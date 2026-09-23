package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A node whose setup fails on a pooled runner is finished as failed with
// the real error at once, rather than sitting claimed until its lease lapses
// and reporting a lost runner minutes later.
func TestPooledNodeSetupFailureFinishesTheNodeWithTheError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	ctrl := client.New(srv.URL, nil)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	claimed, err := ctrl.ClaimNode(ctx, "pool:1", nil, 3*time.Minute, nil)
	if err != nil || claimed == nil {
		t.Fatalf("claim node: %+v, %v", claimed, err)
	}

	saved := runPooledNodeOnce
	t.Cleanup(func() { runPooledNodeOnce = saved })
	runPooledNodeOnce = func(context.Context, string, string, string, string, string, string,
		sparkwing.Logger, *slog.Logger, *orchestrator.LocalAdmission, ...orchestrator.RunNodeOption,
	) (runner.Result, error) {
		return runner.Result{}, errors.New("compile pipeline: exit status 1")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executePooledNode(ctx, ctrl, srv.URL, "", "", "", claimed, claimed.ClaimedBy,
		3*time.Minute, time.Hour, "pool runner", logger, nil, nil)

	got, err := st.GetNode(ctx, "r1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != string(sparkwing.Failed) || !strings.Contains(got.Error, "compile pipeline: exit status 1") {
		t.Fatalf("node after a setup failure = outcome %q error %q, want failed with the setup error", got.Outcome, got.Error)
	}
}
