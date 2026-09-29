package local

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func revokedController(t *testing.T) (*client.Client, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`)
	}))
	t.Cleanup(srv.Close)
	return client.NewWithToken(srv.URL, nil, ""), &requests
}

func TestSuperviseStopsOnADeadToken(t *testing.T) {
	ctrl, requests := revokedController(t)
	r := New(ctrl, Config{SuperviseInterval: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
	bounces := make(chan *store.NodeBounce, 1)

	r.supervise(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"}, bounces)

	if got := requests.Load(); got != 1 {
		t.Fatalf("supervision made %d requests after the token died, want 1", got)
	}
	if len(bounces) != 0 {
		t.Fatal("handed over a bounce after the token died")
	}
}

func TestResultForReadsARefusedTokenAsALostClaim(t *testing.T) {
	ctrl, requests := revokedController(t)
	r := New(ctrl, Config{Logger: slog.New(slog.DiscardHandler)})

	res := r.resultFor(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"}, &exec.Cmd{}, nil, false, nil)

	if res.Outcome != sparkwing.Cancelled || !errors.Is(res.Err, runner.ErrClaimLost) {
		t.Fatalf("result = %s / %v, want a lost claim", res.Outcome, res.Err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("made %d requests; a lost claim writes no row", got)
	}
}
