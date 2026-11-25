package cluster

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	controllerLookupInterval = 200 * time.Millisecond
	controllerLookupLimit    = 5
)

type hostLookup func(ctx context.Context, host string) ([]netip.Addr, error)

// hack: each lookup gets its own Resolver because a shared one joins concurrent lookups of a host into one query.
func lookupController(ctx context.Context, host string) ([]netip.Addr, error) {
	return (&net.Resolver{}).LookupNetIP(ctx, "ip", host)
}

func controllerDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}

// safety: only dials to the controller host are hedged, and TLS still verifies the certificate for the URL's host,
// so a spoofed lookup answer cannot impersonate the controller.
func controllerHTTP(controllerURL string, lookup hostLookup) *http.Client {
	return controllerHTTPWith(controllerURL, lookup, controllerDialer())
}

func controllerHTTPWith(controllerURL string, lookup hostLookup, dialer *net.Dialer) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// hack: a replaced process transport has no dialer to hedge, so it is used unchanged.
		return &http.Client{Timeout: 30 * time.Second, Transport: http.DefaultTransport}
	}
	transport := base.Clone()
	host := ""
	if u, err := url.Parse(controllerURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	transport.DialContext = hedgedDial(dialer, host, lookup)
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

// hack: the hedge only waits out a dropped first lookup; the dial itself stays the standard dialer's, so its
// shared budget, address order and IPv4/IPv6 race are unchanged at the cost of one more, now prompt, lookup.
func hedgedDial(dialer *net.Dialer, controller string, lookup hostLookup) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || controller == "" || !strings.EqualFold(host, controller) {
			return dialer.DialContext(ctx, network, address)
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return dialer.DialContext(ctx, network, address)
		}
		if dialer.Timeout > 0 {
			// safety: one deadline spans the hedged lookup and the dial, as the dialer's timeout spans its own lookup.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, dialer.Timeout)
			defer cancel()
		}
		if err := hedgedLookup(ctx, host, lookup, controllerLookupInterval, controllerLookupLimit); err != nil {
			return nil, &net.OpError{Op: "dial", Net: network, Err: err}
		}
		return dialer.DialContext(ctx, network, address)
	}
}

// hack: works around strict VPC CNI network policy dropping a new Pod's egress until its agent attaches (about 2s),
// where job.go's 1s resolver timeout makes a dropped query cost a second. A fresh lookup every interval answers
// soon after attachment. Remove once the agent attaches before the Pod starts or the cluster uses standard mode.
// safety: lookups are independent, the first success wins, and when all fail the first lookup's error is returned.
func hedgedLookup(ctx context.Context, host string, lookup hostLookup, interval time.Duration, limit int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		attempt int
		err     error
	}
	answers := make(chan answer, limit)
	launched := 0
	launch := func() {
		launched++
		attempt := launched
		go func() {
			addrs, err := lookup(ctx, host)
			if err == nil && len(addrs) == 0 {
				err = &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			answers <- answer{attempt: attempt, err: err}
		}()
	}
	launch()
	hedge := time.NewTicker(interval)
	defer hedge.Stop()
	failed := 0
	var first answer
	for {
		select {
		case a := <-answers:
			if a.err == nil {
				if a.attempt > 1 {
					slog.DebugContext(ctx, "controller lookup answered by a hedge", "attempt", a.attempt, "launched", launched)
				}
				return nil
			}
			failed++
			if first.err == nil || a.attempt < first.attempt {
				first = a
			}
			if failed == launched {
				return first.err
			}
		case <-hedge.C:
			if launched < limit && ctx.Err() == nil {
				launch()
			}
		}
	}
}
