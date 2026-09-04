package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type triggerJoinParent struct{ sparkwing.Base }

func (triggerJoinParent) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", func(context.Context) error { return nil })
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("orch-trigger-join",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &triggerJoinParent{} })
}

func TestRunLocal_TriggerLoopFinishesBeforeTheStoreCloses(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", filepath.Join(t.TempDir(), "profiles.yaml"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	p := PathsAt(t.TempDir())
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}

	// The child's .sparkwing/ compiles rather than resolving from a path, so the
	// dispatch is still running when the parent's own work is done.
	childRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(childRepo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRepo, ".sparkwing", "go.mod"),
		[]byte("module joinchild\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRepo, ".sparkwing", "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-join-child", Pipeline: "join-child", CreatedAt: time.Now(),
		ParentRunID: "run-join-parent",
		TriggerEnv:  map[string]string{SubmitRepoDirKey: childRepo},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := RunLocal(ctx, p, Options{Pipeline: "orch-trigger-join", RunID: "run-join-parent"}); err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	after, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	trig, err := after.GetTrigger(ctx, "run-join-child")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "claimed" {
		t.Fatalf("child trigger status = %q after the parent returned; its bookkeeping was still running when the store closed", trig.Status)
	}
}
