package k8s

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func writeRevoked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "3600")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`)
}

func TestRunNode_MissingJobWithADeadTokenIsALostClaim(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	var dead atomic.Bool
	var nodeReads atomic.Int64
	next := controller.New(st, nil).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dead.Load() {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodes/build") {
			nodeReads.Add(1)
		}
		writeRevoked(w)
	}))
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		dead.Store(true)
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: time.Hour,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Cancelled || !errors.Is(res.Err, runner.ErrClaimLost) {
		t.Fatalf("result = %s / %v, want a lost claim", res.Outcome, res.Err)
	}
	if got := nodeReads.Load(); got != 1 {
		t.Fatalf("read the node %d times after the token died, want 1", got)
	}
	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Outcome != "" {
		t.Fatalf("node outcome = %q; a dispatcher that lost its claim must not write one", n.Outcome)
	}
}

type countingHeartbeater struct {
	calls atomic.Int64
	err   error
}

func (c *countingHeartbeater) TouchNodeHeartbeat(context.Context, string, string) error {
	c.calls.Add(1)
	return c.err
}

func TestHeartbeatLoop_StopsOnADeadToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hb := &countingHeartbeater{err: &client.TokenDeadError{State: "revoked"}}
		heartbeatLoop(t.Context(), hb, "run-1", "build", slog.New(slog.DiscardHandler))
		if got := hb.calls.Load(); got != 1 {
			t.Fatalf("beat %d times on a dead token, want 1", got)
		}
	})
}

func TestHeartbeatLoop_KeepsBeatingThroughOtherFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hb := &countingHeartbeater{err: errors.New("controller 500: boom")}
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
		defer cancel()
		heartbeatLoop(ctx, hb, "run-1", "build", slog.New(slog.DiscardHandler))
		if got := hb.calls.Load(); got != 3 {
			t.Fatalf("beat %d times in 12s through a 500, want 3", got)
		}
	})
}
