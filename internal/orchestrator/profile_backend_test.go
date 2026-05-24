package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// redirectHome points HOME at a temp dir so BuiltinLaptopProfile's
// ~/.cache/sparkwing filesystem surfaces resolve under the test sandbox
// rather than the developer's real home.
func redirectHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestOpenReadBackendForProfile_S3State(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	p := &profile.Profile{
		Name:  "team",
		State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"},
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile: %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.S3Backend); !ok {
		t.Fatalf("backend = %T, want *backend.S3Backend", b)
	}
}

func TestOpenReadBackendForProfile_SQLiteState(t *testing.T) {
	neutralizeEnv(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")
	p := &profile.Profile{
		Name:  "local",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath},
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile: %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.StoreBackend); !ok {
		t.Fatalf("backend = %T, want *backend.StoreBackend", b)
	}
}

func TestOpenReadBackendForProfile_BuiltinLaptop(t *testing.T) {
	neutralizeEnv(t)
	redirectHome(t)
	root := t.TempDir()
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: root}, profile.BuiltinLaptopProfile())
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile(laptop): %v", err)
	}
	defer closer.Close()
	// Laptop state is sqlite with no path; the caller's Paths fills it.
	sb, ok := b.(*backend.StoreBackend)
	if !ok {
		t.Fatalf("backend = %T, want *backend.StoreBackend", b)
	}
	if sb == nil {
		t.Fatal("nil StoreBackend")
	}
}

func TestOpenReadBackendForProfile_ControllerProfile(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{
		Name:       "prod",
		Controller: "https://api.example.dev",
		Token:      "swu_test",
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile(controller): %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.ClientBackend); !ok {
		t.Fatalf("backend = %T, want *backend.ClientBackend", b)
	}
}

func TestApplyProfileBackends_SQLiteState(t *testing.T) {
	neutralizeEnv(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")
	p := &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
	opts := Options{}
	if err := ApplyProfileBackends(context.Background(), &opts, p); err != nil {
		t.Fatalf("ApplyProfileBackends: %v", err)
	}
	if opts.State == nil {
		t.Fatal("State not populated")
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("State = %T, want *store.Store", opts.State)
	}
}

func TestApplyProfileBackends_BuiltinLaptop(t *testing.T) {
	neutralizeEnv(t)
	redirectHome(t)
	opts := Options{DefaultStateDB: filepath.Join(t.TempDir(), "state.db")}
	if err := ApplyProfileBackends(context.Background(), &opts, profile.BuiltinLaptopProfile()); err != nil {
		t.Fatalf("ApplyProfileBackends(laptop): %v", err)
	}
	if opts.State == nil {
		t.Fatal("State nil")
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("State = %T, want *store.Store (sqlite)", opts.State)
	}
	if opts.LogStore == nil {
		t.Error("LogStore nil; laptop declares a filesystem logs surface")
	}
	if opts.ArtifactStore == nil {
		t.Error("ArtifactStore nil; laptop declares a filesystem cache surface")
	}
}

func TestApplyProfileBackends_ControllerProfile(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{Name: "prod", Controller: "https://api.example.dev", Token: "swu_test"}
	opts := Options{}
	if err := ApplyProfileBackends(context.Background(), &opts, p); err != nil {
		t.Fatalf("ApplyProfileBackends(controller): %v", err)
	}
	if opts.State == nil {
		t.Fatal("State nil")
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*client.Client); !ok {
		t.Fatalf("State = %T, want *client.Client", opts.State)
	}
	if opts.LogStore == nil || opts.ArtifactStore == nil {
		t.Error("controller profile should route logs + cache through the controller")
	}
}

func TestApplyProfileBackends_LocalOnlyShortCircuits(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{Name: "prod", Controller: "https://api.example.dev"}
	opts := Options{LocalOnly: true, DefaultStateDB: filepath.Join(t.TempDir(), "state.db")}
	if err := ApplyProfileBackends(context.Background(), &opts, p); err != nil {
		t.Fatalf("ApplyProfileBackends(LocalOnly): %v", err)
	}
	defer opts.State.Close()
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("LocalOnly state = %T, want *store.Store", opts.State)
	}
	if opts.LogStore != nil || opts.ArtifactStore != nil {
		t.Error("LocalOnly should leave logs + cache nil")
	}
}

func TestApplyProfileBackends_RespectsPreSetState(t *testing.T) {
	neutralizeEnv(t)
	preDB := filepath.Join(t.TempDir(), "pre.db")
	pre, err := store.Open(preDB)
	if err != nil {
		t.Fatalf("pre open: %v", err)
	}
	defer pre.Close()
	p := &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(t.TempDir(), "should-not-win.db")}}
	opts := Options{State: pre}
	if err := ApplyProfileBackends(context.Background(), &opts, p); err != nil {
		t.Fatalf("ApplyProfileBackends: %v", err)
	}
	if opts.State != pre {
		t.Error("pre-set State was overwritten")
	}
}
