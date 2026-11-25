package cluster

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

const (
	controllerLookupInterval = 200 * time.Millisecond
	controllerLookupLimit    = 5
)

type hostLookup func(ctx context.Context, host string) ([]netip.Addr, error)

// hack: each query gets its own Resolver because a shared one joins concurrent lookups of a host into one query.
func lookupController(ctx context.Context, host string) ([]netip.Addr, error) {
	return (&net.Resolver{}).LookupNetIP(ctx, "ip", host)
}

// safety: only dials to the controller host are hedged, and TLS still verifies the certificate for the URL's host,
// so a spoofed lookup answer cannot impersonate the controller.
func controllerHTTP(controllerURL string) *http.Client {
	return controllerHTTPWith(controllerURL, lookupController)
}

func controllerHTTPWith(controllerURL string, lookup hostLookup) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// hack: a replaced process transport has no dialer to hedge, so it is used unchanged.
		return &http.Client{Timeout: 30 * time.Second, Transport: http.DefaultTransport}
	}
	transport := base.Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	host := ""
	if u, err := url.Parse(controllerURL); err == nil {
		host = u.Hostname()
	}
	transport.DialContext = hedgedDial(dialer, host, lookup)
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

func hedgedDial(dialer *net.Dialer, controller string, lookup hostLookup) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || controller == "" || host != controller {
			return dialer.DialContext(ctx, network, address)
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return dialer.DialContext(ctx, network, address)
		}
		addrs, err := hedgedLookup(ctx, host, lookup, controllerLookupInterval, controllerLookupLimit)
		if err != nil {
			return nil, &net.OpError{Op: "dial", Net: network, Err: err}
		}
		var dialErr error
		for _, addr := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
		}
		return nil, dialErr
	}
}

// hack: works around strict VPC CNI network policy dropping a new Pod's egress until its agent attaches (about 2s),
// where job.go's 1s resolver timeout makes a dropped query cost a second. A fresh query every interval answers
// soon after attachment. Remove once the agent attaches before the Pod starts or the cluster uses standard mode.
// safety: queries are independent, the first success wins, and when all fail the first query's error is returned.
func hedgedLookup(ctx context.Context, host string, lookup hostLookup, interval time.Duration, limit int) ([]netip.Addr, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		attempt int
		addrs   []netip.Addr
		err     error
	}
	answers := make(chan answer, limit)
	launched := 0
	launch := func() {
		launched++
		attempt := launched
		go func() {
			addrs, err := lookup(ctx, host)
			answers <- answer{attempt: attempt, addrs: addrs, err: err}
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
			if a.err == nil && len(a.addrs) > 0 {
				if launched > 1 {
					slog.DebugContext(ctx, "controller lookup needed hedged queries", "attempts", launched)
				}
				return a.addrs, nil
			}
			if a.err == nil {
				a.err = &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			failed++
			if first.err == nil || a.attempt < first.attempt {
				first = a
			}
			if failed == launched {
				return nil, first.err
			}
		case <-hedge.C:
			if launched < limit && ctx.Err() == nil {
				launch()
			}
		}
	}
}
