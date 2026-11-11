package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestGatewayClaimClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{"html503", 503, "<html>no backend</html>", true},
		{"text502", 502, "no backend", true},
		{"html504", 504, "<html>timeout</html>", true},
		{"text500", 500, "internal error", true},
		{"malformed503", 503, "{broken", true},
		{"unavailable502", 502, `{"error":{"code":"unavailable","message":"Bad Gateway"}}`, true},
		{"unavailable503", 503, `{"error":{"code":"unavailable","message":"Service Unavailable"}}`, true},
		{"duplicate503", 503, `{"error":{},"error":{}}`, false},
		{"html200", 200, "<html>login</html>", false},
		{"html401", 401, "<html>unauthorized</html>", false},
		{"html403", 403, "forbidden", false},
		{"auth401", 401, `{"error":{"code":"unauthenticated","message":"expired"}}`, false},
		{"forbidden403", 403, `{"error":{"code":"forbidden","message":"refused"}}`, false},
		{"protocol503", 503, `{"error":{"code":"protocol_error","message":"incompatible version"}}`, false},
		{"invalidEnvelope502", 502, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := testEngine(t, func(*http.Request) (*http.Response, error) {
				calls++
				resp := engineResponse(tc.status, tc.body)
				resp.Header.Set("Retry-After", "2")
				return resp, nil
			})
			_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
			if err == nil || RetryableTransportError(err) != tc.retry || calls != 1 {
				t.Fatalf("calls=%d retry=%v error=%v", calls, RetryableTransportError(err), err)
			}
			if tc.retry {
				if delay, ok := hosterr.RetryDelay(err); !ok || delay != 2*time.Second || !strings.Contains(err.Error(), http.StatusText(tc.status)) {
					t.Fatalf("gateway status or advice lost: delay=%s ok=%v error=%v", delay, ok, err)
				}
			}
		})
	}
}

func TestGatewaySafeRequestRecovers(t *testing.T) {
	for _, advice := range []string{"", "2", "Mon, 01 Jan 2000 00:00:00 GMT"} {
		t.Run(advice, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				start := time.Now()
				c := testEngine(t, func(*http.Request) (*http.Response, error) {
					calls++
					if calls > 2 {
						data, err := json.Marshal(controllerwire.RunDetail{ID: "run", Number: 1, Status: "running"})
						if err != nil {
							t.Fatal(err)
						}
						return engineResponse(200, string(data)), nil
					}
					resp := engineResponse(503, "<html>no backend</html>")
					resp.Header.Set("Retry-After", advice)
					return resp, nil
				})
				if _, err := c.ReadRun(t.Context(), "run"); err != nil || calls != 3 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 4*time.Second || advice == "2" && elapsed != 4*time.Second {
					t.Fatalf("retry elapsed=%s advice=%q", elapsed, advice)
				}
			})
		})
	}
}

func TestGatewaySafeRequestBoundsRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := testEngine(t, func(*http.Request) (*http.Response, error) { calls++; return engineResponse(502, "no backend"), nil })
		_, err := c.ReadRun(t.Context(), "run")
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != hosterr.Unavailable || calls != 4 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	})
}

func TestGatewayLauncherClaimDoesNotReplay(t *testing.T) {
	calls := 0
	c := testEngine(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/v1/launcher/claim" {
			t.Errorf("path=%s", r.URL.Path)
		}
		return engineResponse(503, "<html>no backend</html>"), nil
	})
	c.launcher = true
	_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
	if !RetryableTransportError(err) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestGatewayHeldReadOutlivesClaimRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		calls := 0
		c, err := NewLocalHostClient(EngineOptions{URL: "http://controller", Bearer: func(context.Context) (string, error) { return "held-capability", nil }, HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if calls <= 16 {
				return engineResponse(503, "<html>no backend</html>"), nil
			}
			data, err := json.Marshal(runstate.RunSnapshot{Run: runstate.RunRow{Execution: runstate.Execution{ID: "run"}}})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(data)), nil
		})}}, "run")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.ReadSnapshot(t.Context(), "run"); err != nil || calls != 17 || time.Since(start) <= 5*time.Second {
			t.Fatalf("calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
		}
	})
}

func TestRetryAfterHTTPDate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resp := engineResponse(503, "no backend")
		defer resp.Body.Close()
		resp.Header.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
		if delay, ok := parseRetryAfter(resp); !ok || delay != 3*time.Second {
			t.Fatalf("delay=%s ok=%v", delay, ok)
		}
	})
}

func TestRetryBackoffBoundsAndJitter(t *testing.T) {
	for _, tc := range []struct {
		attempt          int
		floor, low, high time.Duration
	}{
		{0, 0, 50 * time.Millisecond, 100 * time.Millisecond},
		{1, 0, 100 * time.Millisecond, 200 * time.Millisecond},
		{1000, 0, 2500 * time.Millisecond, 5 * time.Second},
		{1000, 2 * time.Second, 3500 * time.Millisecond, 5 * time.Second},
		{1000, 10 * time.Second, 10 * time.Second, 10 * time.Second},
	} {
		seen := make(map[time.Duration]bool)
		for range 32 {
			delay := RetryBackoff(tc.attempt, tc.floor)
			if delay < tc.low || delay > tc.high {
				t.Fatalf("attempt=%d floor=%s delay=%s", tc.attempt, tc.floor, delay)
			}
			seen[delay] = true
		}
		if tc.low != tc.high && len(seen) < 2 {
			t.Fatalf("attempt=%d backoff has no jitter", tc.attempt)
		}
	}
}
