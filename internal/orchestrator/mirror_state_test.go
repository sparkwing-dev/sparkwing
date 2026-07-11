package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func openMirrorStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mirrorSampleRun(id string) store.Run {
	return store.Run{ID: id, Pipeline: "demo", Status: "running", StartedAt: time.Now()}
}

func TestMirrorState_TeesWrites(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	m := newMirrorStateBackend(localState{st: canon}, local, nil)

	if err := m.CreateRun(ctx, mirrorSampleRun("run-1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for name, s := range map[string]*store.Store{"canonical": canon, "local": local} {
		if got, err := s.GetRun(ctx, "run-1"); err != nil || got == nil {
			t.Fatalf("%s missing the write: %v %#v", name, err, got)
		}
	}
}

func TestMirrorState_LocalFailureToleratedAndLogged(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	if err := local.Close(); err != nil {
		t.Fatalf("close local: %v", err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m := newMirrorStateBackend(localState{st: canon}, local, logger)

	if err := m.CreateRun(ctx, mirrorSampleRun("run-1")); err != nil {
		t.Fatalf("local failure should not surface: %v", err)
	}
	if got, _ := canon.GetRun(ctx, "run-1"); got == nil {
		t.Fatal("canonical write should have succeeded")
	}
	if !strings.Contains(buf.String(), "mirror: local state write failed") || !strings.Contains(buf.String(), "CreateRun") {
		t.Fatalf("expected a warn for the failed local write, got:\n%s", buf.String())
	}
}

func TestMirrorState_CanonicalFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	if err := canon.Close(); err != nil {
		t.Fatalf("close canonical: %v", err)
	}
	m := newMirrorStateBackend(localState{st: canon}, local, nil)

	if err := m.CreateRun(ctx, mirrorSampleRun("run-1")); err == nil {
		t.Fatal("canonical error must surface")
	}
	if got, err := local.GetRun(ctx, "run-1"); err != nil || got == nil {
		t.Fatalf("local should have written despite canonical failure: %v %#v", err, got)
	}
}

func TestMirrorState_ReadsDelegateToCanonical(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	if err := canon.CreateRun(ctx, mirrorSampleRun("run-1")); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	m := newMirrorStateBackend(localState{st: canon}, local, nil)

	if got, err := m.GetRun(ctx, "run-1"); err != nil || got == nil || got.ID != "run-1" {
		t.Fatalf("read should return canonical's value: %v %#v", err, got)
	}
	if got, _ := local.GetRun(ctx, "run-1"); got != nil {
		t.Fatal("read should not have touched local")
	}
}

func TestMirrorState_AppendEventTeed(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	m := newMirrorStateBackend(localState{st: canon}, local, nil)

	if err := m.CreateRun(ctx, mirrorSampleRun("run-1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := m.AppendEvent(ctx, "run-1", "", "run_start", []byte(`{"k":"v"}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	for name, s := range map[string]*store.Store{"canonical": canon, "local": local} {
		evs, err := s.ListEventsAfter(ctx, "run-1", 0, 10)
		if err != nil {
			t.Fatalf("%s ListEventsAfter: %v", name, err)
		}
		if len(evs) == 0 {
			t.Fatalf("%s did not receive the teed event", name)
		}
	}
}

func TestMirrorState_EnqueueTriggerCanonicalOnly(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	m := newMirrorStateBackend(localState{st: canon}, local, nil)

	if _, err := m.EnqueueTrigger(ctx, "demo", nil, "", "", "", "manual", "tester", "", ""); err != nil {
		t.Fatalf("EnqueueTrigger: %v", err)
	}
	canonTriggers, err := canon.ListTriggers(ctx, store.TriggerFilter{})
	if err != nil {
		t.Fatalf("canonical ListTriggers: %v", err)
	}
	if len(canonTriggers) != 1 {
		t.Fatalf("canonical should have 1 trigger, got %d", len(canonTriggers))
	}
	localTriggers, err := local.ListTriggers(ctx, store.TriggerFilter{})
	if err != nil {
		t.Fatalf("local ListTriggers: %v", err)
	}
	if len(localTriggers) != 0 {
		t.Fatalf("EnqueueTrigger must NOT tee to local; got %d local triggers", len(localTriggers))
	}
}

func TestMirrorState_EnqueueTriggerWithEnvCanonicalOnly(t *testing.T) {
	ctx := context.Background()
	canon := openMirrorStore(t)
	local := openMirrorStore(t)
	m := newMirrorStateBackend(localState{st: canon}, local, nil)
	if err := canon.CreateRun(ctx, store.Run{
		ID:        "parent-run",
		Pipeline:  "parent",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed parent run: %v", err)
	}

	triggerID, err := m.EnqueueTriggerWithEnv(ctx,
		"demo", nil, "parent-run", "spawn", "", "await-pipeline", "tester", "", "",
		map[string]string{
			"CHILD_CONTEXT": "from-parent",
		},
	)
	if err != nil {
		t.Fatalf("EnqueueTriggerWithEnv: %v", err)
	}
	trigger, err := canon.GetTrigger(ctx, triggerID)
	if err != nil {
		t.Fatalf("canonical GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["CHILD_CONTEXT"] != "from-parent" {
		t.Fatalf("canonical trigger env = %q", trigger.TriggerEnv["CHILD_CONTEXT"])
	}
	localTriggers, err := local.ListTriggers(ctx, store.TriggerFilter{})
	if err != nil {
		t.Fatalf("local ListTriggers: %v", err)
	}
	if len(localTriggers) != 0 {
		t.Fatalf("EnqueueTriggerWithEnv must NOT tee to local; got %d local triggers", len(localTriggers))
	}
}

type mirrorOKPipe struct{ sparkwing.Base }

func (mirrorOKPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(_ context.Context) error { return nil })
	return nil
}

var mirrorRegisterOnce sync.Once

func registerMirrorTestPipelines() {
	mirrorRegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("mirror-ok",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &mirrorOKPipe{} })
	})
}

func TestRunLocal_MirrorsStateToLocalShadow(t *testing.T) {
	registerMirrorTestPipelines()
	ctrlStore, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatalf("controller store: %v", err)
	}
	t.Cleanup(func() { _ = ctrlStore.Close() })
	srv := httptest.NewServer(controller.New(ctrlStore, nil).Handler())
	t.Cleanup(srv.Close)

	paths := Paths{Root: t.TempDir()}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	local, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })

	res, err := RunLocal(context.Background(), paths, Options{
		Pipeline:    "mirror-ok",
		State:       client.NewWithToken(srv.URL, nil, ""),
		MirrorLocal: local,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	if run, err := ctrlStore.GetRun(context.Background(), res.RunID); err != nil || run == nil {
		t.Fatalf("controller-side run missing: %v %#v", err, run)
	}
	reader, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("reopen local: %v", err)
	}
	defer reader.Close()
	if run, err := reader.GetRun(context.Background(), res.RunID); err != nil || run == nil {
		t.Fatalf("local shadow missing the run: %v %#v", err, run)
	}
}
