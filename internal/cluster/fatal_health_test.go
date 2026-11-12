package cluster

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestRunnerHealthReportsFatalClaimDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started, release, refuse, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		failure := errors.New("malformed controller claim response")
		var hostContext context.Context
		claims := 0
		loop := &runners.Loop{
			Config: runners.LoopConfig{Version: "v1.0.0", MaxConcurrent: 2},
			Controller: cliClaim(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if claims == 1 {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "held", RunID: "run", Capability: "award"}}, nil
				}
				select {
				case <-refuse:
					return controllerwire.ClaimResponse{}, failure
				case <-ctx.Done():
					return controllerwire.ClaimResponse{}, context.Cause(ctx)
				}
			}),
			Host: cliHost(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				hostContext = ctx
				close(started)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}),
			CheckVersion:    func(context.Context, string) error { return nil },
			OnClaimsStopped: func() { close(stopped) },
		}
		listener := newMetricsListener()
		done := make(chan error, 1)
		go func() { done <- runWithMetrics(ctx, listener, loop.Run, loop.HealthError) }()
		client, closeClient := newMetricsHTTPClient(listener)
		defer closeClient()
		<-started
		for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			if status == http.StatusServiceUnavailable {
				close(refuse)
				<-stopped
			}
			response, err := client.Get("http://metrics/healthz")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			if err := errors.Join(readErr, response.Body.Close()); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != status || status == http.StatusServiceUnavailable && (!strings.Contains(string(body), failure.Error()) || !strings.Contains(string(body), "draining 1 held unit")) {
				t.Errorf("health status=%d body=%s, want %d", response.StatusCode, body, status)
			}
		}
		if hostContext.Err() != nil {
			t.Error("health degradation cancelled held work")
		}
		close(release)
		if err := <-done; !errors.Is(err, failure) {
			t.Fatalf("fatal error lost: %v", err)
		}
	})
}
