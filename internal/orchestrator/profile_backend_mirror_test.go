package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/mirror"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestApplyProfileBackendsWithMirror_S3WrapsByDefault(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	p := &profile.Profile{Name: "team", State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}}
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*mirror.Backend); !ok {
		t.Fatalf("State = %T, want *mirror.Backend (s3 is non-local, MirrorLocal defaults true)", opts.State)
	}
}

func TestApplyProfileBackendsWithMirror_S3NoWrapWhenDisabled(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	no := false
	p := &profile.Profile{
		Name:        "team",
		State:       &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"},
		MirrorLocal: &no,
	}
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*mirror.Backend); ok {
		t.Fatal("State should NOT be wrapped when MirrorLocal=false")
	}
	if _, err := os.Stat(paths.StateDB()); !os.IsNotExist(err) {
		t.Errorf("no local sqlite should be created when mirror is disabled; stat err = %v", err)
	}
}

func TestApplyProfileBackendsWithMirror_SqliteLaptopNoWrap(t *testing.T) {
	neutralizeEnv(t)
	redirectHome(t)
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, profile.BuiltinLaptopProfile(), paths); err != nil {
		t.Fatalf("apply(laptop): %v", err)
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("State = %T, want plain *store.Store (laptop is already local)", opts.State)
	}
}

func TestApplyProfileBackendsWithMirror_ControllerWrapsAndWritesBoth(t *testing.T) {
	neutralizeEnv(t)
	var createdRuns int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs" {
			atomic.AddInt32(&createdRuns, 1)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &profile.Profile{Name: "prod", Controller: srv.URL, Token: "swu_test"}
	paths := Paths{Root: t.TempDir()}
	opts := Options{}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply(controller): %v", err)
	}
	if _, ok := opts.State.(*mirror.Backend); !ok {
		t.Fatalf("State = %T, want *mirror.Backend (controller is non-local)", opts.State)
	}

	if err := opts.State.CreateRun(context.Background(), store.Run{ID: "run-1", Pipeline: "demo", Status: "running"}); err != nil {
		t.Fatalf("CreateRun via mirror: %v", err)
	}
	if got := atomic.LoadInt32(&createdRuns); got != 1 {
		t.Errorf("controller (canonical) saw %d creates, want 1", got)
	}
	// Close flushes WAL, then read the local shadow back independently.
	if err := opts.State.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reader, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("reopen local: %v", err)
	}
	defer reader.Close()
	if got, gerr := reader.GetRun(context.Background(), "run-1"); gerr != nil || got == nil {
		t.Fatalf("local shadow missing the run: %v %#v", gerr, got)
	}
}

func TestApplyProfileBackendsWithMirror_LocalOnlyNoWrap(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{Name: "prod", Controller: "https://api.example.dev"}
	paths := Paths{Root: t.TempDir()}
	opts := Options{LocalOnly: true, DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply(LocalOnly): %v", err)
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*mirror.Backend); ok {
		t.Fatal("LocalOnly must never wrap in a mirror")
	}
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("LocalOnly State = %T, want *store.Store", opts.State)
	}
}

func TestApplyProfileBackendsWithMirror_CloseSucceedsOnWrappedState(t *testing.T) {
	neutralizeEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	p := &profile.Profile{Name: "prod", Controller: srv.URL, Token: "swu_test"}
	paths := Paths{Root: t.TempDir()}
	opts := Options{}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := opts.State.(*mirror.Backend); !ok {
		t.Fatalf("State = %T, want *mirror.Backend", opts.State)
	}
	// The run's defer opts.State.Close() must cascade through the mirror
	// to the local store without error.
	if err := opts.State.Close(); err != nil {
		t.Fatalf("Close on wrapped state: %v", err)
	}
	// And the local shadow file exists on disk afterward.
	if _, err := os.Stat(paths.StateDB()); err != nil {
		t.Errorf("expected local shadow db at %s: %v", paths.StateDB(), err)
	}
}
