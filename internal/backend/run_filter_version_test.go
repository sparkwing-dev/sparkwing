package backend

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunFilterVersionFor_AnnouncesWhatTheBackendCanHonour(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := RunFilterVersionFor(NewStoreBackend(st, paths.Paths{Root: t.TempDir()}, nil)); got != store.RunFilterVersion {
		t.Errorf("a store-backed surface announced %q, want %q", got, store.RunFilterVersion)
	}
	if got := RunFilterVersionFor(&S3Backend{}); got == store.RunFilterVersion {
		t.Errorf("a backend that ignores the cursor announced cursor support (%q)", got)
	}
}
