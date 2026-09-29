package warmpool

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRunnerTreatsADeadTokenAsALostClaim(t *testing.T) {
	var nodeLists, revokes atomic.Int64
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, func(next http.Handler, _ *store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch {
			case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/runs/run-1/nodes"):
				nodeLists.Add(1)
			case strings.HasSuffix(req.URL.Path, "/revoke-ready"):
				revokes.Add(1)
			default:
				next.ServeHTTP(w, req)
				return
			}
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`)
		})
	})
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{PollInterval: 5 * time.Millisecond}, quietTestLogger())

	res := r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Cancelled || !errors.Is(res.Err, runner.ErrClaimLost) {
		t.Fatalf("result = %s / %v, want a lost claim", res.Outcome, res.Err)
	}
	if got := nodeLists.Load(); got != 1 {
		t.Fatalf("listed the run's nodes %d times after the token died, want 1", got)
	}
	if fallback.calls.Load() != 0 {
		t.Fatal("ran the node on the fallback after losing the claim")
	}
	if revokes.Load() != 0 {
		t.Fatal("revoked the node's offer after losing the claim")
	}
	n, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.Outcome != "" {
		t.Fatalf("node outcome = %q; a dispatcher that lost its claim must not write one", n.Outcome)
	}
}
