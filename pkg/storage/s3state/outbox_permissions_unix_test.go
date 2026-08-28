//go:build !windows

package s3state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
)

func TestOutboxCreatesPrivateSQLiteFilesAndPreservesRows(t *testing.T) {
	root := t.TempDir()
	artifacts, err := storagefs.NewArtifactStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "outbox.db")
	outbox, err := s3state.OpenOutbox(path, artifacts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Stage(context.Background(), s3state.OutboxKindState, "runs/r/state.ndjson", []byte("state")); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", candidate, got)
		}
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := s3state.OpenOutbox(path, artifacts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if pending, err := reopened.Pending(context.Background()); err != nil || pending != 1 {
		t.Fatalf("pending after hardening and reopen = %d, %v; want 1", pending, err)
	}
}
