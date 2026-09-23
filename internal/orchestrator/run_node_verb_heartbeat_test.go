package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The controller starts billing a dispatched node at the pod's first claim
// renewal, so the pod renews as it starts rather than one interval later,
// which would leave the start of its fetch unbilled.
func TestDispatchedClaimRenewsAsThePodStarts(t *testing.T) {
	renewed := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/run-1/nodes/build/heartbeat" {
			renewed <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		heartbeatDispatchedClaim(ctx, client.NewWithToken(srv.URL, nil, "tok"), "run-1", "build",
			store.NodeClaimFence{HolderID: "k8s-job:x", ClaimGeneration: 1}, time.Minute,
			func(error) {}, slog.New(slog.DiscardHandler))
	}()
	select {
	case <-renewed:
	case <-time.After(store.DispatchedHeartbeatInterval / 2):
		t.Fatalf("no renewal within %s of the pod starting; the first waited for the ticker",
			store.DispatchedHeartbeatInterval/2)
	}
	cancel()
	<-done
}
