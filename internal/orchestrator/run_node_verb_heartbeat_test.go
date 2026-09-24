package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type heartbeatTransport func(*http.Request) (*http.Response, error)

func (f heartbeatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The controller starts billing a dispatched node at the pod's first claim
// renewal, so the pod renews as it starts rather than one interval later,
// which would leave the start of its fetch unbilled.
func TestDispatchedClaimRenewsAsThePodStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		renewed := make(chan struct{}, 1)
		transport := heartbeatTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/run-1/nodes/build/heartbeat" {
				renewed <- struct{}{}
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		start := time.Now()
		go func() {
			defer close(done)
			heartbeatDispatchedClaim(ctx, client.NewWithToken("http://controller.test", &http.Client{Transport: transport}, "tok"), "run-1", "build",
				store.NodeClaimFence{HolderID: "k8s-job:x", ClaimGeneration: 1}, time.Minute,
				func(error) {}, slog.New(slog.DiscardHandler))
		}()
		<-renewed
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("the first renewal waited %s after the pod started", elapsed)
		}
		cancel()
		<-done
	})
}
