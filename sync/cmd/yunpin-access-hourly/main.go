// SPDX-License-Identifier: Apache-2.0

// Command yunpin-access-hourly aggregates one complete UTC hour of the
// relay's already-redacted application stdout access log.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxInputLineBytes = 4096
	accessTimeLayout  = "2006/01/02 15:04:05"
)

var (
	accessLinePattern = regexp.MustCompile(`^yunpin-sync ([0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}) method=([^ ]+) path=([^ ]+) status=([0-9]{3}) duration_ms=([0-9]+)$`)
	fixedRoutes       = map[string]struct{}{
		"/healthz":                 {},
		"/v1/accounts":             {},
		"/v1/accounts/:id":         {},
		"/v1/accounts/:id/claim":   {},
		"/v1/accounts/:id/recover": {},
		"/v1/auth":                 {},
		"/v1/devices":              {},
		"/v1/devices/:id":          {},
		"/v1/keyring":              {},
		"/v1/pairings":             {},
		"/v1/pairings/:id":         {},
		"/v1/pairings/:id/approve": {},
		"/v1/pairings/:id/claim":   {},
		"/v1/sync":                 {},
		"unmatched":                {},
	}
)

type counts struct {
	Total  uint64
	TwoXX  uint64
	FourXX uint64
	FiveXX uint64
	Other  uint64
}

func (counts *counts) add(status int) {
	counts.Total++
	switch status / 100 {
	case 2:
		counts.TwoXX++
	case 4:
		counts.FourXX++
	case 5:
		counts.FiveXX++
	default:
		counts.Other++
	}
}

type routeReport struct {
	Route  string `json:"route"`
	Total  uint64 `json:"total"`
	TwoXX  uint64 `json:"2xx"`
	FourXX uint64 `json:"4xx"`
	FiveXX uint64 `json:"5xx"`
	Other  uint64 `json:"other"`
}

type hourlyReport struct {
	SchemaVersion  int           `json:"schema_version"`
	WindowStartUTC string        `json:"window_start_utc"`
	WindowEndUTC   string        `json:"window_end_utc"`
	Total          uint64        `json:"total"`
	TwoXX          uint64        `json:"2xx"`
	FourXX         uint64        `json:"4xx"`
	FiveXX         uint64        `json:"5xx"`
	Other          uint64        `json:"other"`
	Routes         []routeReport `json:"routes"`
}

func isAccessLike(line string) bool {
	return strings.HasPrefix(line, "yunpin-sync ") ||
		strings.Contains(line, "method=") ||
		strings.Contains(line, "path=") ||
		strings.Contains(line, "status=") ||
		strings.Contains(line, "duration_ms=")
}

func parseUTCHour(value string) (time.Time, error) {
	hour, err := time.Parse(time.RFC3339, value)
	if err != nil || hour.Nanosecond() != 0 || hour.Minute() != 0 || hour.Second() != 0 ||
		value != hour.UTC().Format(time.RFC3339) {
		return time.Time{}, errors.New("hour must be a canonical RFC3339 UTC hour")
	}
	return hour.UTC(), nil
}

func parseAccessLine(line string, lineNumber int) (time.Time, string, int, error) {
	match := accessLinePattern.FindStringSubmatch(line)
	if match == nil {
		return time.Time{}, "", 0, fmt.Errorf("malformed access log at line %d", lineNumber)
	}
	timestamp, err := time.ParseInLocation(accessTimeLayout, match[1], time.UTC)
	if err != nil || timestamp.Format(accessTimeLayout) != match[1] {
		return time.Time{}, "", 0, fmt.Errorf("invalid access timestamp at line %d", lineNumber)
	}
	route := match[3]
	if _, ok := fixedRoutes[route]; !ok {
		return time.Time{}, "", 0, fmt.Errorf("unknown access route at line %d", lineNumber)
	}
	status, err := strconv.Atoi(match[4])
	if err != nil || status < 100 || status > 599 {
		return time.Time{}, "", 0, fmt.Errorf("invalid access status at line %d", lineNumber)
	}
	if _, err := strconv.ParseUint(match[5], 10, 64); err != nil {
		return time.Time{}, "", 0, fmt.Errorf("invalid access duration at line %d", lineNumber)
	}
	return timestamp, route, status, nil
}

func aggregate(input io.Reader, hour time.Time) (hourlyReport, error) {
	end := hour.Add(time.Hour)
	totals := counts{}
	byRoute := make(map[string]*counts)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), maxInputLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !isAccessLike(line) {
			continue
		}
		timestamp, route, status, err := parseAccessLine(line, lineNumber)
		if err != nil {
			return hourlyReport{}, err
		}
		if timestamp.Before(hour) || !timestamp.Before(end) {
			continue
		}
		totals.add(status)
		routeCounts := byRoute[route]
		if routeCounts == nil {
			routeCounts = &counts{}
			byRoute[route] = routeCounts
		}
		routeCounts.add(status)
	}
	if err := scanner.Err(); err != nil {
		return hourlyReport{}, fmt.Errorf("invalid input at line %d", lineNumber+1)
	}

	routes := make([]string, 0, len(byRoute))
	for route := range byRoute {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	routeReports := make([]routeReport, 0, len(routes))
	for _, route := range routes {
		value := byRoute[route]
		routeReports = append(routeReports, routeReport{
			Route: route, Total: value.Total, TwoXX: value.TwoXX,
			FourXX: value.FourXX, FiveXX: value.FiveXX, Other: value.Other,
		})
	}
	return hourlyReport{
		SchemaVersion:  1,
		WindowStartUTC: hour.Format(time.RFC3339),
		WindowEndUTC:   end.Format(time.RFC3339),
		Total:          totals.Total, TwoXX: totals.TwoXX, FourXX: totals.FourXX,
		FiveXX: totals.FiveXX, Other: totals.Other, Routes: routeReports,
	}, nil
}

func run(arguments []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("yunpin-access-hourly", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	hourValue := flags.String("hour", "", "canonical RFC3339 UTC hour")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("usage: yunpin-access-hourly --hour <UTC-hour>")
	}
	hour, err := parseUTCHour(*hourValue)
	if err != nil {
		return err
	}
	report, err := aggregate(input, hour)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return errors.New("encode hourly aggregate")
	}
	encoded = append(encoded, '\n')
	if _, err := io.Copy(output, bytes.NewReader(encoded)); err != nil {
		return errors.New("write hourly aggregate")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "yunpin-access-hourly:", err)
		os.Exit(1)
	}
}
