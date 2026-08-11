// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEndpointPolicyRejectsCredentialLeaksAndPublicHTTP(t *testing.T) {
	for _, raw := range []string{
		"http://example.com:8787",
		"http://nas.local:8787",
		"https://user:secret@example.com",
		"https://example.com/base",
		"https://example.com?token=secret",
		"file:///tmp/yunpin.sock",
	} {
		if _, err := ParseEndpoint(raw, EndpointPolicy{AllowPrivateHTTP: true}); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", raw)
		}
	}
	if _, err := ParseEndpoint("http://192.168.1.127:8787", EndpointPolicy{}); err == nil {
		t.Fatal("private HTTP was accepted without explicit opt-in")
	}
	for _, raw := range []string{
		"https://sync.example.test",
		"http://127.0.0.1:8787",
		"http://192.168.1.127:8787",
		"http://[::1]:8787",
	} {
		endpoint, err := ParseEndpoint(raw, EndpointPolicy{AllowPrivateHTTP: true})
		if err != nil || endpoint.String() != raw {
			t.Fatalf("safe endpoint rejected: raw=%s endpoint=%s err=%v", raw, endpoint.String(), err)
		}
	}
}

func TestEndpointConfigIsStrictAndContainsNoCredentialFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	if err := os.WriteFile(path, []byte(`{"endpoint":"http://192.168.1.127:8787","allow_private_http":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint, err := LoadEndpointConfig(path)
	if err != nil || endpoint.String() != "http://192.168.1.127:8787" {
		t.Fatalf("strict config failed: endpoint=%s err=%v", endpoint.String(), err)
	}
	if err := os.WriteFile(path, []byte(`{"endpoint":"https://example.test","device_token":"forbidden"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEndpointConfig(path); err == nil {
		t.Fatal("credential-bearing unknown field was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte(`{"endpoint":"https://example.test"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEndpointConfig(path); err == nil {
			t.Fatal("group/world-writable endpoint configuration was accepted")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "sync-link.json")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEndpointConfig(link); err == nil {
			t.Fatal("symlinked endpoint configuration was accepted")
		}
	}
}
