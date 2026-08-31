// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func runForHour(t *testing.T, hour, input string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	err := run([]string{"--hour", hour}, strings.NewReader(input), &output)
	return output.String(), err
}

func accessLine(timestamp, method, route string, status int, duration int) string {
	return fmt.Sprintf("yunpin-sync %s method=%s path=%s status=%d duration_ms=%d\n",
		timestamp, method, route, status, duration)
}

func TestHourlyAggregateSeparatesStatusClassesAndSortsRoutes(t *testing.T) {
	input := "2026/08/31 08:00:00 yunpin-sync listening on :8787\n" +
		accessLine("2026/08/31 08:59:59", "GET", "/healthz", 200, 1) +
		accessLine("2026/08/31 09:00:00", "POST", "/v1/sync", 201, 2) +
		accessLine("2026/08/31 09:10:00", "DELETE", "/v1/accounts/:id", 404, 3) +
		accessLine("2026/08/31 09:20:00", "POST", "/v1/sync", 503, 4) +
		accessLine("2026/08/31 09:30:00", "GET", "/healthz", 304, 5) +
		accessLine("2026/08/31 10:00:00", "GET", "/healthz", 200, 6)

	output, err := runForHour(t, "2026-08-31T09:00:00Z", input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"window_start_utc":"2026-08-31T09:00:00Z","window_end_utc":"2026-08-31T10:00:00Z","total":4,"2xx":1,"4xx":1,"5xx":1,"other":1,"routes":[{"route":"/healthz","total":1,"2xx":0,"4xx":0,"5xx":0,"other":1},{"route":"/v1/accounts/:id","total":1,"2xx":0,"4xx":1,"5xx":0,"other":0},{"route":"/v1/sync","total":2,"2xx":1,"4xx":0,"5xx":1,"other":0}]}` + "\n"
	if output != want {
		t.Fatalf("aggregate output differs:\n got %s\nwant %s", output, want)
	}
	for _, forbidden := range []string{"method", "duration", "POST", "DELETE"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("aggregate exposed %q", forbidden)
		}
	}
}

func TestHourlyAggregateUsesHalfOpenWindowAndFreshCounts(t *testing.T) {
	input := accessLine("2026/08/31 09:59:59", "POST", "/v1/sync", 200, 1) +
		accessLine("2026/08/31 10:00:00", "POST", "/v1/accounts", 400, 1) +
		accessLine("2026/08/31 11:00:00", "POST", "/v1/sync", 500, 1)
	output, err := runForHour(t, "2026-08-31T10:00:00Z", input)
	if err != nil {
		t.Fatal(err)
	}
	var report hourlyReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || report.TwoXX != 0 || report.FourXX != 1 ||
		report.FiveXX != 0 || report.Other != 0 || len(report.Routes) != 1 ||
		report.Routes[0].Route != "/v1/accounts" {
		t.Fatalf("window did not reset at the UTC boundary: %#v", report)
	}
}

func TestHTTPMethodIsNeitherGroupedNorRestrictedToKnownVerbs(t *testing.T) {
	input := accessLine("2026/08/31 09:00:00", "M-SEARCH", "unmatched", 405, 1)
	output, err := runForHour(t, "2026-08-31T09:00:00Z", input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "M-SEARCH") || !strings.Contains(output, `"4xx":1`) {
		t.Fatalf("method affected or entered aggregate output: %s", output)
	}
}

func TestAllCurrentFixedRouteLabelsAreAccepted(t *testing.T) {
	routes := make([]string, 0, len(fixedRoutes))
	for route := range fixedRoutes {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	var input strings.Builder
	for index, route := range routes {
		input.WriteString(accessLine(
			fmt.Sprintf("2026/08/31 09:00:%02d", index), "GET", route, 200, index,
		))
	}
	output, err := runForHour(t, "2026-08-31T09:00:00Z", input.String())
	if err != nil {
		t.Fatal(err)
	}
	var report hourlyReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Total != uint64(len(routes)) || report.TwoXX != uint64(len(routes)) ||
		len(report.Routes) != len(routes) {
		t.Fatalf("fixed route coverage differs: %#v", report)
	}
	for index, route := range routes {
		if report.Routes[index].Route != route {
			t.Fatalf("route %d=%q, want %q", index, report.Routes[index].Route, route)
		}
	}
}

func TestFixedRoutesMatchServerRouteLabels(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../internal/server/server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var routeFunction *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "routeLabel" {
			routeFunction = function
			break
		}
	}
	if routeFunction == nil {
		t.Fatal("server routeLabel function is missing")
	}
	serverRoutes := make(map[string]struct{})
	ast.Inspect(routeFunction.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.ReturnStmt)
		if !ok || len(statement.Results) != 1 {
			return true
		}
		literal, ok := statement.Results[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(literal.Value)
		if unquoteErr != nil {
			t.Fatalf("routeLabel has an invalid string literal: %v", unquoteErr)
		}
		serverRoutes[value] = struct{}{}
		return true
	})
	if len(serverRoutes) != len(fixedRoutes) {
		t.Fatalf("collector routes=%d server routes=%d", len(fixedRoutes), len(serverRoutes))
	}
	for route := range serverRoutes {
		if _, ok := fixedRoutes[route]; !ok {
			t.Fatalf("collector is missing fixed server route %q", route)
		}
	}
}

func TestMalformedOrUnknownAccessLineFailsWithoutEcho(t *testing.T) {
	secretID := strings.Repeat("a", 32)
	secretToken := "synthetic-token-must-not-appear"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "dynamic route",
			input: accessLine("2026/08/31 09:00:00", "GET",
				"/v1/accounts/"+secretID+"?token="+secretToken, 401, 1),
			want: "unknown access route at line 1",
		},
		{
			name: "malformed fields",
			input: "ignored startup line\n" +
				"yunpin-sync 2026/08/31 09:00:00 method=POST path=/v1/sync status=200 token=" + secretToken + "\n",
			want: "malformed access log at line 2",
		},
		{
			name:  "invalid timestamp",
			input: accessLine("2026/13/31 09:00:00", "GET", "/healthz", 200, 1),
			want:  "invalid access timestamp at line 1",
		},
		{
			name:  "invalid status",
			input: accessLine("2026/08/31 09:00:00", "GET", "/healthz", 699, 1),
			want:  "invalid access status at line 1",
		},
		{
			name: "invalid duration",
			input: "yunpin-sync 2026/08/31 09:00:00 method=GET path=/healthz " +
				"status=200 duration_ms=999999999999999999999999999999999999\n",
			want: "invalid access duration at line 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runForHour(t, "2026-08-31T09:00:00Z", test.input)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			if output != "" {
				t.Fatalf("failed aggregation emitted partial output: %q", output)
			}
			combined := output + err.Error()
			for _, secret := range []string{secretID, secretToken} {
				if strings.Contains(combined, secret) {
					t.Fatalf("failed aggregation echoed a synthetic secret")
				}
			}
		})
	}
}

func TestOversizedInputLineFailsGenerically(t *testing.T) {
	secret := "synthetic-oversized-token"
	input := "method=" + strings.Repeat("X", maxInputLineBytes) + secret
	output, err := runForHour(t, "2026-08-31T09:00:00Z", input)
	if err == nil || err.Error() != "invalid input at line 1" {
		t.Fatalf("oversized input error=%v", err)
	}
	if output != "" || strings.Contains(err.Error(), secret) {
		t.Fatal("oversized input was emitted")
	}
}

func TestHourMustBeCanonicalRFC3339UTCStart(t *testing.T) {
	invalid := []string{
		"", "2026-08-31T09:00:01Z", "2026-08-31T09:01:00Z",
		"2026-08-31T09:00:00+00:00", "2026-08-31T09:00:00+01:00",
		"2026-08-31T09:00:00.000Z",
	}
	for _, hour := range invalid {
		t.Run(hour, func(t *testing.T) {
			output, err := runForHour(t, hour, "")
			if err == nil || output != "" {
				t.Fatalf("invalid hour accepted: output=%q err=%v", output, err)
			}
			if hour != "" && strings.Contains(err.Error(), hour) {
				t.Fatal("invalid hour value was echoed")
			}
		})
	}
}

func TestNonAccessTextIsIgnoredAndOutputIsDeterministic(t *testing.T) {
	input := "2026/08/31 09:00:00 yunpin-sync listening on :8787\n" +
		"synthetic raw endpoint query token id must stay ignored\n"
	first, err := runForHour(t, "2026-08-31T09:00:00Z", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runForHour(t, "2026-08-31T09:00:00Z", input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same input produced different output:\n%s\n%s", first, second)
	}
	for _, forbidden := range []string{"endpoint", "query", "token", " id "} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("aggregate emitted ignored input text %q", forbidden)
		}
	}
	want := `{"schema_version":1,"window_start_utc":"2026-08-31T09:00:00Z","window_end_utc":"2026-08-31T10:00:00Z","total":0,"2xx":0,"4xx":0,"5xx":0,"other":0,"routes":[]}` + "\n"
	if first != want {
		t.Fatalf("empty aggregate differs: got %s want %s", first, want)
	}
}
