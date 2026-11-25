package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var (
	droppedAnswer = netip.MustParseAddr("192.0.2.1")
	hedgedAnswer  = netip.MustParseAddr("192.0.2.2")
)

func TestHedgedLookupAnswersWithinOneIntervalWhenFirstQueryIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		firstCanceled := make(chan struct{})
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			if calls.Add(1) == 1 {
				// hack: the resolver's own retry answers a dropped query after its 1s timeout.
				select {
				case <-time.After(time.Second):
					return []netip.Addr{droppedAnswer}, nil
				case <-ctx.Done():
					close(firstCanceled)
					return nil, ctx.Err()
				}
			}
			return []netip.Addr{hedgedAnswer}, nil
		}
		started := time.Now()
		addrs, err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed != controllerLookupInterval {
			t.Fatalf("lookup took %s, want one hedge interval %s", elapsed, controllerLookupInterval)
		}
		if len(addrs) != 1 || addrs[0] != hedgedAnswer {
			t.Fatalf("addresses = %v, want only the answering query's %v", addrs, hedgedAnswer)
		}
		synctest.Wait()
		select {
		case <-firstCanceled:
		default:
			t.Fatal("the outstanding first query was not canceled")
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("queries = %d, want 2", got)
		}
	})
}

func TestHedgedLookupReturnsTheFirstQueryErrorWhenEveryQueryFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstErr := &net.DNSError{Err: "i/o timeout", Name: "controller.test", IsTimeout: true}
		laterErr := errors.New("later query failed")
		var calls atomic.Int32
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			if calls.Add(1) == 1 {
				time.Sleep(300 * time.Millisecond)
				return nil, firstErr
			}
			return nil, laterErr
		}
		addrs, err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit)
		if !errors.Is(err, firstErr) || addrs != nil {
			t.Fatalf("lookup = %v, %v; want the first query's error and no addresses", addrs, err)
		}
	})
}

func TestHedgedLookupReturnsAnImmediateResolverErrorWithoutHedging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		notFound := &net.DNSError{Err: "no such host", Name: "controller.test", IsNotFound: true}
		var calls atomic.Int32
		lookup := func(context.Context, string) ([]netip.Addr, error) {
			calls.Add(1)
			return nil, notFound
		}
		_, err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit)
		if !errors.Is(err, notFound) {
			t.Fatalf("error = %v, want %v", err, notFound)
		}
		time.Sleep(time.Second)
		if got := calls.Load(); got != 1 {
			t.Fatalf("queries = %d, want 1", got)
		}
	})
}

func TestHedgedLookupStopsHedgingWhenCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, active atomic.Int32
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			calls.Add(1)
			active.Add(1)
			defer active.Add(-1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(500*time.Millisecond, cancel)
		_, err := hedgedLookup(ctx, "controller.test", lookup, controllerLookupInterval, controllerLookupLimit)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got != 3 {
			t.Fatalf("queries = %d, want 3 launched before cancellation and none after", got)
		}
		if got := active.Load(); got != 0 {
			t.Fatalf("%d queries still running after cancellation", got)
		}
	})
}

func TestHedgedLookupCapsOutstandingQueries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if _, err := hedgedLookup(ctx, "controller.test", lookup, controllerLookupInterval, controllerLookupLimit); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want the deadline", err)
		}
		if got := calls.Load(); got != controllerLookupLimit {
			t.Fatalf("queries = %d, want %d", got, controllerLookupLimit)
		}
	})
}

func TestControllerHTTPVerifiesTheControllerCertificateAfterHedgedLookup(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	served, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	loopback := netip.MustParseAddr("127.0.0.1")
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	request := func(host string) error {
		var mu sync.Mutex
		var looked []string
		controller := "https://" + net.JoinHostPort(host, served.Port())
		client := controllerHTTPWith(controller, func(_ context.Context, name string) ([]netip.Addr, error) {
			mu.Lock()
			looked = append(looked, name)
			mu.Unlock()
			return []netip.Addr{loopback}, nil
		})
		client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
		response, err := client.Get(controller + "/")
		if err == nil {
			_ = response.Body.Close()
		}
		mu.Lock()
		defer mu.Unlock()
		if len(looked) != 1 || looked[0] != host {
			t.Fatalf("lookups = %v, want one for %s", looked, host)
		}
		return err
	}
	// safety: the test certificate names example.com, so only that host may be answered by this server.
	if err := request("example.com"); err != nil {
		t.Fatalf("controller named by the certificate: %v", err)
	}
	err = request("controller.test")
	if _, ok := errors.AsType[x509.HostnameError](err); !ok {
		t.Fatalf("address answered for another name: error = %v, want a certificate hostname refusal", err)
	}
}

func TestControllerHTTPDialsOtherHostsWithoutTheHedgedLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	var calls atomic.Int32
	client := controllerHTTPWith("https://controller.test", func(context.Context, string) ([]netip.Addr, error) {
		calls.Add(1)
		return nil, errors.New("unexpected lookup")
	})
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := calls.Load(); got != 0 {
		t.Fatalf("hedged lookups = %d for a non-controller host, want 0", got)
	}
}

func TestControllerHTTPReportsALookupFailureAsADialError(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "controller.test", IsNotFound: true}
	client := controllerHTTPWith("https://controller.test", func(context.Context, string) ([]netip.Addr, error) {
		return nil, notFound
	})
	response, err := client.Get("https://controller.test/")
	if err == nil {
		_ = response.Body.Close()
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" || !errors.Is(err, notFound) {
		t.Fatalf("error = %v, want a dial error wrapping the resolver error", err)
	}
	if !strings.Contains(err.Error(), "dial tcp: lookup controller.test") {
		t.Fatalf("error text = %q, want the standard dial lookup wording", err)
	}
}
