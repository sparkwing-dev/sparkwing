package runners

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func claimRecoveryResponse(t *testing.T, status int, value any, advice string) *http.Response {
	body := "no backend"
	if status == 200 {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = string(b)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{advice}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestLoopDeferredClaimSuccessClearsDegradation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		claims, renewals := 0, 0
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
		defer cancel()
		started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var heldContext context.Context
		var loop *Loop
		client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/heartbeat") {
				renewals++
				return claimRecoveryResponse(t, 200, controllerwire.RenewalResponse{ExpiresAt: time.Now().Add(3 * time.Minute)}, ""), nil
			}
			claims++
			if claims == 1 {
				return claimRecoveryResponse(t, 200, controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "held", RunID: "run", Capability: "award"}}, ""), nil
			}
			<-started
			if time.Since(start) < 12*time.Minute {
				return claimRecoveryResponse(t, 503, nil, "60"), nil
			}
			loop.Drain()
			<-stopped
			return claimRecoveryResponse(t, 200, controllerwire.ClaimResponse{}, ""), nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, OnClaimsStopped: func() { close(stopped) }, Host: hostFunc(func(ctx context.Context, award controllerwire.ClaimedUnit) error {
			ctx, stopAward := award.HostContext(ctx)
			defer stopAward()
			keeper := lease.Keeper{Interval: time.Minute, Lease: 3 * time.Minute, Renew: func(ctx context.Context) (lease.Beat, error) {
				beat, err := client.Renew(ctx, award.UnitID, controllerwire.RenewalRequest{})
				return lease.Beat{ExpiresAt: beat.ExpiresAt}, err
			}}
			ctx, stopKeeper := keeper.Run(ctx)
			defer stopKeeper()
			heldContext = ctx
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		})}
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()
		<-stopped
		synctest.Wait()
		health := loop.HealthError()
		t.Logf("successful recovery accepted during drain: elapsed=%s claims=%d renewals=%d held_live=%v health=%v", time.Since(start), claims, renewals, heldContext.Err() == nil, health)
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if health != nil {
			t.Fatal("accepted successful claim left stale degraded health during held-host drain")
		}
	})
}

func TestLoopCapsClaimRetryAdviceAtFiveMinutes(t *testing.T) {
	for _, advice := range []string{"184467440737095516160000", "Fri, 31 Dec 9999 23:59:59 GMT", "body", "rate-limited"} {
		t.Run(advice, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				calls := 0
				ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
				defer cancel()
				var loop *Loop
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						if advice == "body" || advice == "rate-limited" {
							status, code := http.StatusServiceUnavailable, "unavailable"
							if advice == "rate-limited" {
								status, code = http.StatusTooManyRequests, "rate_limited"
							}
							return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"wait","details":{"retry_after_ms":8640000000000000}}}`))}, nil
						}
						return claimRecoveryResponse(t, http.StatusServiceUnavailable, nil, advice), nil
					}
					loop.Drain()
					return claimRecoveryResponse(t, http.StatusOK, controllerwire.ClaimResponse{}, ""), nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				loop = &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil })}
				err = loop.Run(ctx)
				if err != nil || calls != 2 || time.Since(start) != 5*time.Minute {
					t.Fatalf("calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
				}
			})
		})
	}
}
