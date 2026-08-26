package orchestrator

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestCoordinatedLogBackend_NoLogsSurfaceKeepsTheRunsOwnFiles pins the
// laptop case byte-for-byte.
//
// A profile that names no logs surface must leave the node process on
// its caller's default, which is the run's local log files. Anything
// else would move a laptop run's node logs the moment its nodes started
// executing in their own processes.
func TestCoordinatedLogBackend_NoLogsSurfaceKeepsTheRunsOwnFiles(t *testing.T) {
	ctx := context.Background()

	for name, prof := range map[string]*profile.Profile{
		"no profile at all": nil,
		"sqlite state only": {
			Name:  "laptop",
			State: &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(t.TempDir(), "state.db")},
		},
	} {
		got, err := coordinatedLogBackend(ctx, prof)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != nil {
			t.Errorf("%s: opened a %T log backend; want nil so the caller keeps the run's local files", name, got)
		}
	}
}

// TestCoordinatedLogBackend_UnopenableSurfaceFailsTheNode pins the
// fail-fast.
//
// A node that cannot reach the run's declared logs surface must fail,
// not quietly write to the worker's disk: half a run's logs in the
// declared place and half somewhere nobody named is worse than a run
// that stops and says so. The message has to name the profile and the
// surface, because the operator reading it is looking at a node row,
// not at this code.
func TestCoordinatedLogBackend_UnopenableSurfaceFailsTheNode(t *testing.T) {
	_, err := coordinatedLogBackend(context.Background(), &profile.Profile{
		Name:  "shared-team",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(t.TempDir(), "state.db")},
		Logs:  &backends.Spec{Type: backends.TypeGCS, Bucket: "team"},
	})
	if err == nil {
		t.Fatal("an unopenable logs surface did not fail the node")
	}
	for _, want := range []string{"shared-team", "gcs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestStartRunLoopback_MirroredRunTeesChildWritesToBothStores is the
// mirror case: `sparkwing run --profile <remote>` from a laptop, whose
// state is the remote backend with a local SQLite shadow.
//
// Before node bodies moved into their own processes the tee saw every
// write, because the dispatcher made them. A child pointed at the
// canonical handle underneath the tee would have written past the
// mirror, and the laptop's shadow of the run would have carried the
// run row and nothing else. This asserts the child's writes land in
// both stores.
func TestStartRunLoopback_MirroredRunTeesChildWritesToBothStores(t *testing.T) {
	paths := newInternalPaths(t)

	canonicalStore, err := store.Open(filepath.Join(t.TempDir(), "canonical.db"))
	if err != nil {
		t.Fatalf("open canonical store: %v", err)
	}
	t.Cleanup(func() { _ = canonicalStore.Close() })
	srv := httptest.NewServer(controller.New(canonicalStore, quietTestLogger()).Handler())
	t.Cleanup(srv.Close)
	canonical := client.NewWithToken(srv.URL, nil, "")

	mirror, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open mirror store: %v", err)
	}
	t.Cleanup(func() { _ = mirror.Close() })

	ctx := context.Background()
	const runID = "run-mirrored"
	backends := RemoteBackends(canonical, nil, nil, nil, store.DefaultConcurrencyLease)
	backends.State = newMirrorStateBackend(backends.State, mirror, quietTestLogger())
	if err := backends.State.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "mirrored", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	loopback, err := startRunLoopback(&Options{State: canonical, RunID: runID}, backends, quietTestLogger())
	if err != nil {
		t.Fatalf("startRunLoopback: %v", err)
	}
	t.Cleanup(loopback.Close)

	child := client.NewWithToken(loopback.url, nil, loopback.token)
	if err := child.CreateNode(ctx, store.Node{RunID: runID, NodeID: "n", Status: "pending"}); err != nil {
		t.Fatalf("child CreateNode: %v", err)
	}
	if err := child.FinishNode(ctx, runID, "n", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("child FinishNode: %v", err)
	}

	for name, st := range map[string]*store.Store{"canonical": canonicalStore, "mirror": mirror} {
		n, gerr := st.GetNode(ctx, runID, "n")
		if gerr != nil {
			t.Fatalf("%s store: read node: %v", name, gerr)
		}
		if n.Outcome != "success" {
			t.Errorf("%s store: node outcome = %q, want success", name, n.Outcome)
		}
		if string(n.Output) != `{"ok":true}` {
			t.Errorf("%s store: node output = %s", name, n.Output)
		}
	}
}
