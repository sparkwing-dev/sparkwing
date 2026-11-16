package runners

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func TestLoopFatalClaimFailureSurfacesWithoutInterruptingHeldWork(t *testing.T) {
	for _, held := range []int{0, 2} {
		t.Run(fmt.Sprintf("held=%d", held), func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
				defer cancel()
				started, release := make([]chan struct{}, held), make([]chan struct{}, held)
				hostContexts := make([]context.Context, held)
				for i := range held {
					started[i], release[i] = make(chan struct{}), make(chan struct{})
				}
				var claims, renewals atomic.Int64
				client, err := controllerclient.NewEngineClient(controllerclient.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/heartbeat") {
						renewals.Add(1)
						return claimRecoveryResponse(t, 200, controllerwire.RenewalResponse{ExpiresAt: time.Now().Add(3 * time.Minute)}, ""), nil
					}
					claim := int(claims.Add(1))
					if claim <= held {
						return claimRecoveryResponse(t, 200, controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: fmt.Sprintf("held-%d", claim-1), RunID: "run", Capability: "award"}}, ""), nil
					}
					for _, ready := range started {
						<-ready
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<html>invalid claim response</html>"))}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				stopped := make(chan struct{})
				loop := &Loop{Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: held + 1}, Controller: client, CheckVersion: func(context.Context, string) error { return nil }, OnClaimsStopped: func() { close(stopped) }, Host: hostFunc(func(ctx context.Context, award controllerwire.ClaimedUnit) error {
					index := 0
					if _, err := fmt.Sscanf(award.UnitID, "held-%d", &index); err != nil || index >= held {
						return fmt.Errorf("unexpected award %q", award.UnitID)
					}
					ctx, stopAward := award.HostContext(ctx)
					defer stopAward()
					keeper := lease.Keeper{Interval: time.Minute, Lease: 3 * time.Minute, Renew: func(ctx context.Context) (lease.Beat, error) {
						beat, err := client.Renew(ctx, award.UnitID, controllerwire.RenewalRequest{})
						return lease.Beat{ExpiresAt: beat.ExpiresAt}, err
					}}
					ctx, stopKeeper := keeper.Run(ctx)
					defer stopKeeper()
					hostContexts[index] = ctx
					close(started[index])
					select {
					case <-release[index]:
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
				if health == nil || !strings.Contains(health.Error(), "claiming stopped:") || !strings.Contains(health.Error(), fmt.Sprintf("draining %d held units", held)) || time.Since(start) != 0 {
					t.Errorf("fatal intake not immediately visible: health=%v elapsed=%s", health, time.Since(start))
				}
				if held > 0 {
					time.Sleep(5 * time.Minute)
					synctest.Wait()
					for i, host := range hostContexts {
						if host.Err() != nil {
							t.Errorf("valid host %d interrupted: %v", i, context.Cause(host))
						}
					}
					close(release[0])
					synctest.Wait()
					if health := loop.HealthError(); health == nil || !strings.Contains(health.Error(), "draining 1 held unit") {
						t.Errorf("completed host still counted: %v", health)
					}
					time.Sleep(5 * time.Minute)
					synctest.Wait()
					if hostContexts[1].Err() != nil || renewals.Load() < 10 {
						t.Errorf("remaining host lost authority: %v renewals=%d", hostContexts[1].Err(), renewals.Load())
					}
					close(release[1])
				}
				if err := <-done; err == nil || !strings.Contains(err.Error(), "claim runner work") || claims.Load() != int64(held+1) {
					t.Errorf("fatal result lost or claims retried: error=%v claims=%d", err, claims.Load())
				}
				if held == 0 && time.Since(start) != 0 {
					t.Errorf("idle fatal exit delayed: %s", time.Since(start))
				}
			})
			if got := strings.Count(logs.String(), `"level":"ERROR"`); got != 1 {
				t.Errorf("fatal error logged %d times, want once: %s", got, logs.String())
			}
			wantWarnings := 0
			if held > 0 {
				wantWarnings = 2
			}
			if got := strings.Count(logs.String(), `"level":"WARN"`); got != wantWarnings {
				t.Errorf("drain warnings=%d, want %d: %s", got, wantWarnings, logs.String())
			}
		})
	}
}
