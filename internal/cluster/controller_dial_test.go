package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

var loopback = []netip.Addr{netip.MustParseAddr("127.0.0.1")}

func TestHedgedLookupAnswersWithinOneIntervalWhenFirstLookupIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		firstCanceled := make(chan struct{})
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			if calls.Add(1) == 1 {
				// hack: the resolver's own retry answers a dropped query after its 1s timeout.
				select {
				case <-time.After(time.Second):
					return loopback, nil
				case <-ctx.Done():
					close(firstCanceled)
					return nil, ctx.Err()
				}
			}
			return loopback, nil
		}
		started := time.Now()
		err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed != controllerLookupInterval {
			t.Fatalf("lookup took %s, want one hedge interval %s", elapsed, controllerLookupInterval)
		}
		synctest.Wait()
		select {
		case <-firstCanceled:
		default:
			t.Fatal("the outstanding first lookup was not canceled")
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("lookups = %d, want 2", got)
		}
	})
}

func TestHedgedLookupReturnsTheFirstLookupErrorWhenEveryLookupFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstErr := &net.DNSError{Err: "i/o timeout", Name: "controller.test", IsTimeout: true}
		laterErr := errors.New("later lookup failed")
		var calls atomic.Int32
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			if calls.Add(1) == 1 {
				time.Sleep(300 * time.Millisecond)
				return nil, firstErr
			}
			return nil, laterErr
		}
		if err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit); !errors.Is(err, firstErr) {
			t.Fatalf("error = %v, want the first lookup's error", err)
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
		if err := hedgedLookup(t.Context(), "controller.test", lookup, controllerLookupInterval, controllerLookupLimit); !errors.Is(err, notFound) {
			t.Fatalf("error = %v, want %v", err, notFound)
		}
		time.Sleep(time.Second)
		if got := calls.Load(); got != 1 {
			t.Fatalf("lookups = %d, want 1", got)
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
		if err := hedgedLookup(ctx, "controller.test", lookup, controllerLookupInterval, controllerLookupLimit); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got != 3 {
			t.Fatalf("lookups = %d, want 3 launched before cancellation and none after", got)
		}
		if got := active.Load(); got != 0 {
			t.Fatalf("%d lookups still running after cancellation", got)
		}
	})
}

func TestHedgedLookupCapsOutstandingLookups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if err := hedgedLookup(ctx, "controller.test", lookup, controllerLookupInterval, controllerLookupLimit); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want the deadline", err)
		}
		if got := calls.Load(); got != controllerLookupLimit {
			t.Fatalf("lookups = %d, want %d", got, controllerLookupLimit)
		}
	})
}

func TestProductionBootstrapControllerClientHedgesADroppedLookup(t *testing.T) {
	productionDockerFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	served, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	dropped := make(chan struct{})
	lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host != "localhost" {
			t.Errorf("hedged lookup for %q, want the controller host", host)
		}
		if lookups.Add(1) == 1 {
			<-ctx.Done()
			close(dropped)
			return nil, ctx.Err()
		}
		return loopback, nil
	}
	controller := "http://localhost:" + served.Port()
	host, closeBinding, err := productionRuntimeWith("v1.2.3", lookup).Bootstrap.Host(t.Context(), BootstrapOptions{ControllerURL: controller, ModuleCache: filepath.Join(home, "modules")})
	if closeBinding != nil {
		defer func() { _ = closeBinding() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	if host.HTTP == nil {
		t.Fatal("the bootstrap exchange uses a controller client without the hedged lookup")
	}
	if hosting, ok := host.Host.(runners.Hosting); !ok || hosting.HTTP != host.HTTP {
		t.Fatal("renewal and hosting do not share the bootstrap's controller client")
	}
	response, err := host.HTTP.Get(controller + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case <-dropped:
	case <-t.Context().Done():
		t.Fatal("the dropped first lookup was never canceled")
	}
	if lookups.Load() != 2 || requests.Load() != 1 {
		t.Fatalf("lookups=%d requests=%d, want a hedge to answer for the dropped lookup and one request", lookups.Load(), requests.Load())
	}
}

// hack: answers every A and AAAA question with the given addresses over the stream framing Go uses on a net.Pipe.
func fakeDNS(t *testing.T, v4, v6 netip.Addr) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		conn, server := net.Pipe()
		go func() {
			defer server.Close()
			for {
				var size [2]byte
				if _, err := io.ReadFull(server, size[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(server, query); err != nil {
					return
				}
				end := 12
				for query[end] != 0 {
					end += int(query[end]) + 1
				}
				qtype := binary.BigEndian.Uint16(query[end+1:])
				end += 5
				var rdata []byte
				if qtype == 1 && v4.IsValid() {
					rdata = v4.AsSlice()
				} else if qtype == 28 && v6.IsValid() {
					rdata = v6.AsSlice()
				}
				response := append([]byte{query[0], query[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}, query[12:end]...)
				if rdata != nil {
					response[7] = 1
					response = append(response, 0xc0, 0x0c, byte(qtype>>8), byte(qtype), 0, 1, 0, 0, 0, 60, 0, byte(len(rdata)))
					response = append(response, rdata...)
				}
				binary.BigEndian.PutUint16(size[:], uint16(len(response)))
				if _, err := server.Write(append(size[:], response...)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		return conn, nil
	}}
}

func directClient(c *http.Client) *http.Client {
	c.Transport.(*http.Transport).Proxy = nil
	return c
}

func answerLookup(context.Context, string) ([]netip.Addr, error) { return loopback, nil }

func TestControllerHTTPFallsBackFromAStalledAddressLikeTheStandardDialer(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	v4, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := v4.Addr().(*net.TCPAddr).Port
	v6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		_ = v4.Close()
		t.Skipf("IPv6 loopback unavailable on port %d: %v", port, err)
	}
	for _, listener := range []net.Listener{v4, v6} {
		server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
		go func() { _ = server.Serve(listener) }()
		defer server.Close()
	}
	var mu sync.Mutex
	var stalled string
	dialer := controllerDialer()
	dialer.Resolver = fakeDNS(t, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1"))
	dialer.ControlContext = func(ctx context.Context, _, address string, _ syscall.RawConn) error {
		mu.Lock()
		first := stalled == ""
		if first {
			stalled = address
		}
		mu.Unlock()
		if first {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	controller := "http://dual.test:" + strconv.Itoa(port)
	c := directClient(controllerHTTPWith(controller, answerLookup, dialer))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, controller+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Do(request)
	if err != nil {
		t.Fatalf("a stalled address blocked the reachable one: %v", err)
	}
	_ = response.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if stalled == "" {
		t.Fatal("no address stalled, so the fallback was not exercised")
	}
}

func TestControllerHTTPKeepsADialTimeoutRetryable(t *testing.T) {
	dialer := controllerDialer()
	dialer.Timeout = 100 * time.Millisecond
	dialer.Resolver = fakeDNS(t, netip.MustParseAddr("127.0.0.1"), netip.Addr{})
	dialer.ControlContext = func(ctx context.Context, _, _ string, _ syscall.RawConn) error {
		<-ctx.Done()
		return ctx.Err()
	}
	c := directClient(controllerHTTPWith("http://controller.test", answerLookup, dialer))
	response, err := c.Get("http://controller.test:1/")
	if err == nil {
		_ = response.Body.Close()
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !urlErr.Timeout() {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if !client.RetryableTransportError(err) {
		t.Fatalf("dial timeout %v is no longer a retryable transport error", err)
	}
}

func TestControllerHTTPVerifiesTheControllerCertificateAfterHedgedLookup(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	served, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	request := func(host string) error {
		var lookups atomic.Int32
		dialer := controllerDialer()
		dialer.Resolver = fakeDNS(t, netip.MustParseAddr("127.0.0.1"), netip.Addr{})
		controller := "https://" + net.JoinHostPort(host, served.Port())
		c := directClient(controllerHTTPWith(controller, func(context.Context, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return loopback, nil
		}, dialer))
		c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
		response, err := c.Get(controller + "/")
		if err == nil {
			_ = response.Body.Close()
		}
		if lookups.Load() != 1 {
			t.Fatalf("hedged lookups for %s = %d, want 1", host, lookups.Load())
		}
		return err
	}
	// safety: the test certificate names example.com, so only that host may be answered by this server.
	if err := request("example.com"); err != nil {
		t.Fatalf("controller named by the certificate: %v", err)
	}
	if _, ok := errors.AsType[x509.HostnameError](request("controller.test")); !ok {
		t.Fatal("an address answered for another name was not refused by certificate verification")
	}
}

func TestControllerHTTPHedgesTheControllerHostRegardlessOfCase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	served, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	dialer := controllerDialer()
	dialer.Resolver = fakeDNS(t, netip.MustParseAddr("127.0.0.1"), netip.Addr{})
	controller := "http://Controller.TEST:" + served.Port()
	c := directClient(controllerHTTPWith(controller, func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return loopback, nil
	}, dialer))
	response, err := c.Get(controller + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if lookups.Load() != 1 {
		t.Fatalf("hedged lookups = %d for a mixed-case controller host, want 1", lookups.Load())
	}
}

func TestControllerHTTPDialsOtherHostsWithoutTheHedgedLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	served, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	dialer := controllerDialer()
	dialer.Resolver = fakeDNS(t, netip.MustParseAddr("127.0.0.1"), netip.Addr{})
	c := directClient(controllerHTTPWith("https://controller.test", func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return nil, errors.New("unexpected lookup")
	}, dialer))
	response, err := c.Get("http://storage.test:" + served.Port() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := lookups.Load(); got != 0 {
		t.Fatalf("hedged lookups = %d for a non-controller host, want 0", got)
	}
}

func TestControllerHTTPReportsALookupFailureAsADialError(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "controller.test", IsNotFound: true}
	c := directClient(controllerHTTPWith("https://controller.test", func(context.Context, string) ([]netip.Addr, error) {
		return nil, notFound
	}, controllerDialer()))
	response, err := c.Get("https://controller.test/")
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
