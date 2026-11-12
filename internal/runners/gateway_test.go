package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type gatewayTransport func(*http.Request) (*http.Response, error)

func (f gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoopClaimsAfterGatewayOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls, hosted := 0, 0
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{
			URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/v1/runners/claim" {
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
				calls++
				status, body := http.StatusServiceUnavailable, "<html>no backend</html>"
				switch calls {
				case 2:
					status, body = http.StatusBadGateway, "no backend"
				case 3:
					status = http.StatusGatewayTimeout
				case 4:
					status = http.StatusOK
					data, marshalErr := json.Marshal(controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(data)
				default:
					if calls > 4 {
						t.Fatal("claim replayed or loop failed to stop")
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		loop := &Loop{
			Config:     LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 1, PollInterval: time.Second},
			Controller: client, CheckVersion: func(context.Context, string) error { return nil },
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { hosted++; return nil }),
		}
		if err := loop.Run(t.Context()); err != nil || calls != 4 || hosted != 1 || time.Since(start) < 3*time.Second {
			t.Fatalf("claims=%d hosted=%d elapsed=%s error=%v", calls, hosted, time.Since(start), err)
		}
	})
}

func TestLoopGatewayRetryTiming(t *testing.T) {
	for _, tc := range []struct {
		name, advice string
		failures     int
		fatal        bool
	}{
		{"exponential", "", 3, false},
		{"retryAfter", "2", 2, false},
		{"retryAfterExceedsBudget", "3600", 1, true},
		{"boundedOutage", "", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var times []time.Time
				var loop *Loop
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
					if len(times) >= 300 {
						t.Fatal("gateway retry is spinning hot")
					}
					times = append(times, time.Now())
					status, body := 503, "<html>no backend</html>"
					if len(times) > tc.failures {
						status, body = 200, "{}"
						loop.Drain()
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{tc.advice}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Millisecond}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil })}
				start := time.Now()
				err = loop.Run(t.Context())
				if (err != nil) != tc.fatal || time.Since(start) > 10*time.Minute {
					t.Fatalf("calls=%d elapsed=%s error=%v", len(times), time.Since(start), err)
				}
				if tc.fatal {
					if time.Since(start) != 10*time.Minute {
						t.Fatalf("recovery ended after %s, want 10m", time.Since(start))
					}
					return
				}
				for i := 1; i <= tc.failures; i++ {
					gap := times[i].Sub(times[i-1])
					if tc.advice == "2" {
						if gap < 2*time.Second {
							t.Fatalf("retry ignored advice: %s", gap)
						}
						continue
					}
					low, high := []time.Duration{51 * time.Millisecond, 101 * time.Millisecond, 201 * time.Millisecond}[i-1], []time.Duration{101 * time.Millisecond, 201 * time.Millisecond, 401 * time.Millisecond}[i-1]
					if gap < low || gap > high {
						t.Fatalf("retry %d gap=%s want [%s,%s]", i, gap, low, high)
					}
				}
			})
		})
	}
}

func TestLoopGatewayProtocolFailuresStayFatal(t *testing.T) {
	for _, status := range []int{200, 401, 403, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<html>refused</html>"))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			loop := &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { return errors.New("unexpected work") })}
			if err := loop.Run(t.Context()); err == nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestLoopBoundsSlowGatewayRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Timeout: 20 * time.Minute, Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("no backend"))}, nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}})
		if err != nil {
			t.Fatal(err)
		}
		loop := &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Millisecond}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { return errors.New("unexpected work") })}
		start := time.Now()
		if err := loop.Run(t.Context()); !errors.Is(err, context.DeadlineExceeded) || calls != 2 || time.Since(start) != 10*time.Minute {
			t.Fatalf("calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
		}
	})
}

func TestLoopGatewayOutageKeepsHeldHost(t *testing.T) {
	started, release, recovered, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var loop *Loop
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if err := json.NewEncoder(w).Encode(controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}); err != nil {
				t.Error(err)
			}
			return
		}
		<-started
		if calls <= 3 {
			http.Error(w, "<html>no backend</html>", http.StatusServiceUnavailable)
			return
		}
		if err := json.NewEncoder(w).Encode(controllerwire.ClaimResponse{}); err != nil {
			t.Error(err)
		}
		close(recovered)
		loop.Drain()
	}))
	defer server.Close()
	client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	loop = &Loop{
		Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Millisecond}, Controller: client,
		CheckVersion: func(context.Context, string) error { return nil }, OnClaimsStopped: func() { close(stopped) },
		Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
			close(started)
			<-release
			return ctx.Err()
		}),
	}
	joined := make(chan error, 1)
	go func() { joined <- loop.Run(t.Context()) }()
	select {
	case <-recovered:
	case <-stopped:
		select {
		case <-recovered:
		default:
			t.Error("claims stopped before the gateway recovered")
		}
	}
	<-stopped
	select {
	case err := <-joined:
		t.Errorf("runner returned before joining held host: %v", err)
	default:
	}
	close(release)
	if err := <-joined; err != nil || calls != 4 {
		t.Fatalf("claims=%d error=%v", calls, err)
	}
}

func TestLoopSurvivesDeployLengthGatewayOutage(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint("held=", held), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				started, release := make(chan struct{}), make(chan struct{})
				var loop *Loop
				calls, hosted := 0, 0
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
					calls++
					status, body := 503, "no backend"
					if held && calls == 1 {
						data, marshalErr := json.Marshal(controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "held", RunID: "run", Capability: "secret"}})
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						status, body = 200, string(data)
					} else {
						if held {
							<-started
						}
						if time.Since(start) >= 90*time.Second {
							status, body = 200, "{}"
							loop.Drain()
							close(release)
						}
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"10"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Millisecond}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
					hosted++
					close(started)
					select {
					case <-release:
						return ctx.Err()
					case <-ctx.Done():
						return ctx.Err()
					}
				})}
				// safety: release the held fixture even when a premature claim shutdown is the regression.
				loop.OnClaimsStopped = func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}
				if err := loop.Run(t.Context()); err != nil || time.Since(start) < 90*time.Second || calls > 11 || hosted != map[bool]int{false: 0, true: 1}[held] {
					t.Fatalf("held=%v calls=%d hosted=%d elapsed=%s error=%v", held, calls, hosted, time.Since(start), err)
				}
			})
		})
	}
}
