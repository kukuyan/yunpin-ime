// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredTrustedProxyAppliesBeforeAuthLimiterWithoutLeakingHeaders(t *testing.T) {
	var logs bytes.Buffer
	application, err := New(context.Background(), filepath.Join(t.TempDir(), "relay.db"), &logs)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if err := application.ConfigureTrustedProxies("172.17.0.11"); err != nil {
		t.Fatal(err)
	}
	application.authLimiter.limit = 2
	for index, client := range []string{"203.0.113.1", "203.0.113.1", "203.0.113.1", "203.0.113.2"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"x","password":"not-a-secret-fixture"}`))
		request.RemoteAddr = "172.17.0.11:1234"
		request.Header.Set("X-Forwarded-For", client)
		recorder := httptest.NewRecorder()
		application.ServeHTTP(recorder, request)
		want := http.StatusUnauthorized
		if index == 2 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("request %d: status=%d want=%d", index, recorder.Code, want)
		}
	}
	if strings.Contains(logs.String(), "203.0.113.") || strings.Contains(logs.String(), "not-a-secret-fixture") {
		t.Fatal("forwarded identity or request body leaked to application logs")
	}
}

func TestTrustedProxyConfigurationRejectsBroadOrInvalidTrust(t *testing.T) {
	for _, input := range []string{"0.0.0.0", "::", "172.17.0.0/16", "*", "proxy.local", "172.17.0.11,", "fe80::1%en0", "224.0.0.1", strings.Repeat("127.0.0.1,", 17)} {
		if _, err := parseTrustedProxyPeers(input); err == nil {
			t.Errorf("accepted unsafe config %q", input)
		}
	}
	for _, input := range []string{"", "172.17.0.11", " 172.17.0.11,::1 ", "::ffff:172.17.0.11"} {
		if _, err := parseTrustedProxyPeers(input); err != nil {
			t.Errorf("rejected explicit peers %q: %v", input, err)
		}
	}
}

func TestTrustedProxyUsesOnlySingleAddressFromExactPeer(t *testing.T) {
	peers, _ := parseTrustedProxyPeers("172.17.0.11,::1")
	for _, tc := range []struct {
		name, remote string
		headers      []string
		want         string
	}{
		{"untrusted spoof", "198.51.100.10:555", []string{"203.0.113.9"}, "198.51.100.10"},
		{"neighbor spoof", "172.17.0.12:555", []string{"203.0.113.9"}, "172.17.0.12"},
		{"trusted ipv4", "172.17.0.11:555", []string{"203.0.113.9"}, "203.0.113.9"},
		{"mapped peer", "[::ffff:172.17.0.11]:555", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"ipv6", "[::1]:555", []string{"2001:db8::1"}, "2001:db8::1"},
		{"missing", "172.17.0.11:555", nil, "172.17.0.11"},
		{"chain", "172.17.0.11:555", []string{"203.0.113.9, 192.0.2.3"}, "172.17.0.11"},
		{"duplicates", "172.17.0.11:555", []string{"203.0.113.9", "192.0.2.3"}, "172.17.0.11"},
		{"port", "172.17.0.11:555", []string{"203.0.113.9:22"}, "172.17.0.11"},
		{"zone", "172.17.0.11:555", []string{"fe80::1%eth0"}, "172.17.0.11"},
		{"unspecified", "172.17.0.11:555", []string{"0.0.0.0"}, "172.17.0.11"},
		{"malformed peer", "not-an-address", []string{"203.0.113.9"}, "not-an-address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.RemoteAddr = tc.remote
			request.Header["X-Forwarded-For"] = tc.headers
			request.Header.Set("X-Real-IP", "192.0.2.200")
			request.Header.Set("Forwarded", "for=192.0.2.201")
			var got string
			handler := withTrustedProxyPeers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = clientIP(r) }), peers)
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if got != tc.want || request.RemoteAddr != tc.remote {
				t.Fatalf("IP=%s want=%s original=%s", got, tc.want, request.RemoteAddr)
			}
		})
	}
}

func TestTrustedProxyRateLimitsRemainSeparateAndSpoofResistant(t *testing.T) {
	peers, _ := parseTrustedProxyPeers("172.17.0.11")
	limiter := ipLimiter{entries: make(map[string]rateEntry), limit: 2, window: time.Minute}
	now := time.Unix(1000, 0)
	handler := withTrustedProxyPeers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r), now) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), peers)
	check := func(remote, forwarded string, want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
		request.RemoteAddr = remote
		request.Header.Set("X-Forwarded-For", forwarded)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("status=%d want=%d", recorder.Code, want)
		}
	}
	check("172.17.0.11:1", "203.0.113.1", 204)
	check("172.17.0.11:1", "203.0.113.1", 204)
	check("172.17.0.11:1", "203.0.113.1", 429)
	check("172.17.0.11:1", "203.0.113.2", 204)
	check("198.51.100.9:1", "203.0.113.3", 204)
	check("198.51.100.9:2", "203.0.113.4", 204)
	check("198.51.100.9:3", "203.0.113.5", 429)
}
