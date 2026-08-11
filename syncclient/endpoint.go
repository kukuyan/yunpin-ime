// SPDX-License-Identifier: Apache-2.0
// Package syncclient implements the network-only background synchronization
// path shared by desktop adapters. It is never called by an input key handler.
package syncclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxEndpointConfigBytes = 4096

// EndpointPolicy requires an explicit opt-in before plaintext HTTP can be used
// even on loopback or an IP address in a private range. Public HTTP is never
// accepted because bearer tokens would otherwise cross the network in clear.
type EndpointPolicy struct {
	AllowPrivateHTTP bool `json:"allow_private_http"`
}

type Endpoint struct{ base *url.URL }

func (endpoint Endpoint) String() string {
	if endpoint.base == nil {
		return ""
	}
	return endpoint.base.String()
}

func (endpoint Endpoint) resolve(path string) string {
	resolved := *endpoint.base
	resolved.Path = path
	return resolved.String()
}

func ParseEndpoint(raw string, policy EndpointPolicy) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Endpoint{}, errors.New("sync endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return Endpoint{}, errors.New("sync endpoint must not contain credentials, a path, query, or fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Path = ""
	switch parsed.Scheme {
	case "https":
	case "http":
		if !policy.AllowPrivateHTTP {
			return Endpoint{}, errors.New("plaintext HTTP requires allow_private_http")
		}
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || (!ip.IsLoopback() && !ip.IsPrivate())) {
			return Endpoint{}, errors.New("plaintext HTTP is restricted to localhost or a private IP literal")
		}
	default:
		return Endpoint{}, errors.New("sync endpoint scheme must be HTTP or HTTPS")
	}
	return Endpoint{base: parsed}, nil
}

type EndpointConfig struct {
	Endpoint string `json:"endpoint"`
	EndpointPolicy
}

// LoadEndpointConfig reads a non-secret desktop configuration file. Symlinks,
// non-regular files, oversized documents and unknown JSON fields are rejected.
// Tokens and keys belong in DPAPI/Credential Manager or Apple Keychain and are
// intentionally not fields in this document.
func LoadEndpointConfig(path string) (Endpoint, error) {
	if strings.TrimSpace(path) == "" {
		return Endpoint{}, errors.New("sync endpoint configuration path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Endpoint{}, fmt.Errorf("read sync endpoint configuration metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxEndpointConfigBytes {
		return Endpoint{}, errors.New("sync endpoint configuration must be a small regular file, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return Endpoint{}, errors.New("sync endpoint configuration must not be group- or world-writable")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Endpoint{}, fmt.Errorf("open sync endpoint configuration: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return Endpoint{}, errors.New("sync endpoint configuration changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxEndpointConfigBytes+1))
	decoder.DisallowUnknownFields()
	var config EndpointConfig
	if err := decoder.Decode(&config); err != nil {
		return Endpoint{}, errors.New("invalid sync endpoint configuration")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Endpoint{}, errors.New("invalid sync endpoint configuration")
	}
	return ParseEndpoint(config.Endpoint, config.EndpointPolicy)
}
