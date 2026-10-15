package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
