package runners

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
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
	}{
		{"exponential", "", 3},
		{"retryAfter", "2", 2},
		{"longRetryAfter", "3600", 1},
		{"prolongedOutage", "", 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var times []time.Time
				var loop *Loop
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
					if len(times) > tc.failures+1 {
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
				if err != nil || len(times) != tc.failures+1 {
					t.Fatalf("calls=%d elapsed=%s error=%v", len(times), time.Since(start), err)
				}
				if tc.name == "longRetryAfter" {
					if time.Since(start) != 5*time.Minute {
						t.Fatalf("claim advice not capped at five minutes: %s", time.Since(start))
					}
					return
				}
				if tc.name == "prolongedOutage" {
					if time.Since(start) <= 10*time.Minute {
						t.Fatalf("outage too short: %s", time.Since(start))
					}
					for i := 1; i < len(times); i++ {
						if gap := times[i].Sub(times[i-1]); gap < time.Millisecond || gap > 30*time.Second {
							t.Fatalf("unbounded retry cadence: %s", gap)
						}
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

func TestLoopSlowGatewayRecoveryHonorsCallerCancellation(t *testing.T) {
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
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
		defer cancel()
		if err := loop.Run(ctx); !errors.Is(err, context.DeadlineExceeded) || calls != 2 || time.Since(start) != 12*time.Minute {
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
				started := make(chan struct{})
				var loop *Loop
				calls, hosted, reportAttempts, acknowledgements := 0, 0, 0, 0
				transport := &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
					status, body := 503, "no backend"
					if r.URL.Path == "/api/v1/runs/run/finish" {
						reportAttempts++
						var completion controllerwire.FinishRequest
						if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
							t.Fatal(err)
						}
						if r.Header.Get("Authorization") != "Bearer held-award" || completion.Reason != "finished" {
							t.Fatal("completion lost its award or payload")
						}
						if time.Since(start) >= 90*time.Second {
							acknowledgements++
							data, err := json.Marshal(controllerwire.Applied{})
							if err != nil {
								t.Fatal(err)
							}
							status, body = 200, string(data)
						}
					} else {
						calls++
						if held && calls == 1 {
							data, err := json.Marshal(controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "held", RunID: "run", Capability: "held-award"}})
							if err != nil {
								t.Fatal(err)
							}
							status, body = 200, string(data)
						} else {
							if held {
								<-started
							}
							if time.Since(start) >= 90*time.Second {
								status, body = 200, "{}"
								loop.Drain()
							}
						}
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"10"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: transport})
				if err != nil {
					t.Fatal(err)
				}
				loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Millisecond}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
					hosted++
					close(started)
					award, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: transport, Bearer: func(context.Context) (string, error) { return unit.Capability, nil }})
					if err != nil {
						return err
					}
					_, err = award.Finish(ctx, "run", controllerwire.FinishRequest{Reason: "finished"})
					return err
				})}
				expected := map[bool]int{false: 0, true: 1}[held]
				if err := loop.Run(t.Context()); err != nil || time.Since(start) < 90*time.Second || calls > 11 || hosted != expected || acknowledgements != expected || held && reportAttempts < 2 {
					t.Fatalf("held=%v claims=%d hosted=%d reports=%d acks=%d elapsed=%s error=%v", held, calls, hosted, reportAttempts, acknowledgements, time.Since(start), err)
				}
			})
		})
	}
}

func TestLoopClaimSuccessResetsDegradation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, reset := time.Now(), false
		var loop *Loop
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
			if loop.HealthError() != nil {
				t.Error("successful claim did not reset degradation")
			}
			status := 503
			if time.Since(start) >= 18*time.Minute {
				loop.Drain()
				status = 200
			} else if time.Since(start) >= 9*time.Minute && !reset {
				reset = true
				status = 200
			}
			body := "no backend"
			if status == 200 {
				body = "{}"
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Millisecond}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Fatal("unexpected work"); return nil })}
		if err := loop.Run(t.Context()); err != nil || !reset || time.Since(start) < 18*time.Minute {
			t.Fatalf("elapsed=%s reset=%v error=%v", time.Since(start), reset, err)
		}
	})
}

func TestLoopClaimsAfterProlongedOutageWithHeldWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 13*time.Minute)
		defer cancel()
		start := time.Now()
		started, release := make(chan struct{}), make(chan struct{})
		claims, renewals, hosted := 0, 0, 0
		degraded := false
		var loop *Loop
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
			status := 200
			var body any
			if strings.HasSuffix(r.URL.Path, "/heartbeat") {
				renewals++
				body = controllerwire.RenewalResponse{ExpiresAt: time.Now().Add(3 * time.Minute)}
			} else {
				claims++
				if claims == 1 {
					body = controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "held", RunID: "run", Capability: "award"}}
				} else {
					<-started
					if time.Since(start) < 12*time.Minute {
						status = 503
					} else {
						degraded = loop.HealthError() != nil
						body = controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "recovered", RunID: "run", Capability: "award"}}
					}
				}
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if status == 503 {
				encoded = []byte("no backend")
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"10"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, MaxClaims: 2, PollInterval: time.Second}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(ctx context.Context, award controllerwire.ClaimedUnit) error {
			hosted++
			if award.UnitID == "recovered" {
				close(release)
				return nil
			}
			ctx, stopAward := award.HostContext(ctx)
			defer stopAward()
			keeper := lease.Keeper{Interval: time.Minute, Lease: 3 * time.Minute, Renew: func(ctx context.Context) (lease.Beat, error) {
				beat, err := client.Renew(ctx, award.UnitID, controllerwire.RenewalRequest{})
				return lease.Beat{ExpiresAt: beat.ExpiresAt}, err
			}}
			ctx, stopKeeper := keeper.Run(ctx)
			defer stopKeeper()
			close(started)
			select {
			case <-release:
				return context.Cause(ctx)
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		})}
		err = loop.Run(ctx)
		if err != nil || hosted != 2 || renewals < 10 || !degraded || loop.HealthError() != nil {
			t.Fatalf("hosted=%d renewals=%d degraded=%v health=%v claims=%d elapsed=%s error=%v", hosted, renewals, degraded, loop.HealthError(), claims, time.Since(start), err)
		}
	})
}

func TestLoopWarnsRateLimitedDuringAdvisedOutage(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		calls := 0
		var loop *Loop
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
			calls++
			status, body := 503, "no backend"
			if time.Since(start) >= 20*time.Minute {
				status, body = 200, "{}"
				loop.Drain()
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"1200"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil })}
		if err := loop.Run(t.Context()); err != nil || calls != 5 || time.Since(start) != 20*time.Minute || loop.HealthError() != nil {
			t.Fatalf("calls=%d elapsed=%s health=%v error=%v", calls, time.Since(start), loop.HealthError(), err)
		}
	})
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) < 2 || len(lines) > 3 {
		t.Fatalf("unbounded or missing warnings: %s", logs.String())
	}
	for i, line := range lines {
		var entry struct {
			Level string
			Time  time.Time
			Since time.Time
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Level != "WARN" || entry.Time.Sub(entry.Since) != time.Duration(10+5*i)*time.Minute {
			t.Fatalf("warning cadence: %s", line)
		}
	}
}
