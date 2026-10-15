package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func neutralizeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_ACTIONS", "KUBERNETES_SERVICE_HOST", "XDG_CONFIG_HOME",
	} {
		os.Unsetenv(k)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

func TestOpenReadBackendForProfile_ControllerProfile(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_test"},
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

func TestResolveLocalBackends_OpensOnlyState(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_URL", "bogus-scheme://removed")
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: filepath.Join(paths.Root, "state.db")}
	if err := resolveLocalBackends(&opts, false); err != nil {
		t.Fatalf("resolveLocalBackends: %v", err)
	}
	st, ok := opts.State.(*store.Store)
	if !ok {
		t.Fatalf("State = %T, want *store.Store", opts.State)
	}
	defer st.Close()
	if _, err := os.Stat(filepath.Join(paths.Root, "cache")); !os.IsNotExist(err) {
		t.Fatalf("opened the retired artifact cache: %v", err)
	}
}

func TestResolveLocalBackends_RespectsPreSetState(t *testing.T) {
	pre, err := store.Open(filepath.Join(t.TempDir(), "pre.db"))
	if err != nil {
		t.Fatalf("pre open: %v", err)
	}
	defer pre.Close()
	paths := Paths{Root: t.TempDir()}
	opts := Options{State: pre, DefaultStateDB: filepath.Join(paths.Root, "should-not-win.db")}
	if err := resolveLocalBackends(&opts, false); err != nil {
		t.Fatalf("resolveLocalBackends: %v", err)
	}
	if opts.State != pre {
		t.Error("pre-set State was overwritten")
	}
}
