// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ConfigureTrustedProxies must be called before serving requests. Only explicit
// proxy peer IPs are accepted, not broad LAN/Docker CIDRs or a request header
// claiming to be a proxy. An empty configuration keeps the direct-only default.
// The edge must overwrite X-Forwarded-For with exactly one client address.
// This changes rate-limit attribution, never account/device authentication.
func (s *Server) ConfigureTrustedProxies(addresses string) error {
	peers, err := parseTrustedProxyPeers(addresses)
	if err != nil {
		return err
	}
	s.handler = withTrustedProxyPeers(s.handler, peers)
	return nil
}

func parseTrustedProxyPeers(addresses string) (map[netip.Addr]struct{}, error) {
	peers := make(map[netip.Addr]struct{})
	if strings.TrimSpace(addresses) == "" {
		return peers, nil
	}
	values := strings.Split(addresses, ",")
	if len(values) > 16 {
		return nil, errors.New("trusted proxy configuration exceeds the peer limit")
	}
	for _, value := range values {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || !validForwardedAddress(address) {
			return nil, errors.New("trusted proxy configuration requires explicit unicast IP addresses")
		}
		peers[address.Unmap()] = struct{}{}
	}
	return peers, nil
}

func validForwardedAddress(address netip.Addr) bool {
	return address.IsValid() && address.Zone() == "" && !address.IsUnspecified() && !address.IsMulticast()
}

func withTrustedProxyPeers(next http.Handler, peers map[netip.Addr]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err == nil {
			if _, trusted := peers[peer.Addr().Unmap()]; trusted {
				values := r.Header.Values("X-Forwarded-For")
				// Invalid/ambiguous headers fall back to the actual peer bucket;
				// they must never enable arbitrary buckets or bypass throttling.
				if len(values) == 1 && len(values[0]) <= 64 {
					address, parseErr := netip.ParseAddr(strings.TrimSpace(values[0]))
					if parseErr == nil && validForwardedAddress(address) {
						forwarded := new(http.Request)
						*forwarded = *r
						forwarded.RemoteAddr = net.JoinHostPort(address.Unmap().String(), "0")
						next.ServeHTTP(w, forwarded)
						return
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
