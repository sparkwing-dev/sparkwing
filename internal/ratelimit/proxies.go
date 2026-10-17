package ratelimit

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

type proxyClientKey struct{}

// TrustedProxies resolves forwarded clients only across configured proxy networks.
func TrustedProxies(next http.Handler, prefixes []netip.Prefix) http.Handler {
	prefixes = slices.Clone(prefixes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if client := forwardedClient(r, prefixes); client.IsValid() {
			r = r.WithContext(context.WithValue(r.Context(), proxyClientKey{}, client.String()))
		}
		next.ServeHTTP(w, r)
	})
}

func forwardedClient(r *http.Request, prefixes []netip.Prefix) netip.Addr {
	peer, ok := remoteIP(r.RemoteAddr)
	if !ok || !trustedProxy(peer, prefixes) {
		return netip.Addr{}
	}
	chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	client := peer
	for i := len(chain) - 1; i >= 0 && trustedProxy(client, prefixes); i-- {
		address, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil || address.Zone() != "" {
			return peer
		}
		client = address.Unmap()
	}
	return client
}

func trustedProxy(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
