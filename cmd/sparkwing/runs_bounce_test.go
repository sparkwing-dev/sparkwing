package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// bounceHome seeds a sparkwing home holding one live run with a
// running job and one that never started -- the shapes the verb has to
// tell apart.
func bounceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	for _, id := range []string{"build", "deploy"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
	}
	if err := st.StartNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	return home
}

// The verb records the request against the home the run is using and
// says so. It does not wait for the restart: what it writes is an
// intent, and the runner holding the process is what acts on it.
func TestRunsBounce_RecordsTheRequestAgainstTheHomesStore(t *testing.T) {
	home := bounceHome(t)
	if err := runRunsBounce(context.Background(),
		[]string{"--run", "run-1", "--node", "build", "--home", home}); err != nil {
		t.Fatalf("runs bounce: %v", err)
	}

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = st.Close() }()
	b, err := st.PendingNodeBounce(context.Background(), "run-1", "build")
	if err != nil || b == nil {
		t.Fatalf("PendingNodeBounce = %v, %v; want the recorded request", b, err)
	}
	if b.RequestedBy == "" {
		t.Error("request records no requester; the run's history cannot say who asked")
	}
}

// Every refusal names what is actually wrong. An operator reaching for
// this verb is already dealing with a stuck run, and "not found" for a
// job that simply has not started yet would send them hunting for a
// typo they did not make.
func TestRunsBounce_RefusalsSayWhichThingIsWrong(t *testing.T) {
	home := bounceHome(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown run", []string{"--run", "run-9", "--node", "build"}, "run run-9: not found"},
		{"unknown node", []string{"--run", "run-1", "--node", "ghost"}, "node ghost in run run-1: not found"},
		{"job never started", []string{"--run", "run-1", "--node", "deploy"}, "not running"},
		{"no target", []string{"--run", "run-1"}, "--run RUN_ID and --node NODE_ID are required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runRunsBounce(ctx, append(tc.args, "--home", home))
			if err == nil {
				t.Fatalf("bounce %v succeeded; want a refusal", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A finished run has nothing executing it, so the verb says that
// rather than recording an intent nobody will ever consume.
func TestRunsBounce_RefusesAFinishedRun(t *testing.T) {
	home := bounceHome(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.FinishRun(ctx, "run-1", "success", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	_ = st.Close()

	err = runRunsBounce(ctx, []string{"--run", "run-1", "--node", "build", "--home", home})
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Errorf("error = %v, want it to name the run as already finished", err)
	}
}
