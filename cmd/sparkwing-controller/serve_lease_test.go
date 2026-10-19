package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A second controller on the same store stands by instead of serving beside
// the first, and takes over once the first lets go.
func TestServeLeaseKeepsOneControllerServing(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	first, err := acquireServeLease(t.Context(), st, "first", logger)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireServeLease(waiting, st, "second", logger); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second controller = %v, want it standing by", err)
	}
	if !bytes.Contains(logs.Bytes(), []byte("standing by")) {
		t.Fatalf("stand-by not logged: %s", logs.String())
	}
	if err := st.AsOperator().ReleaseSingletonLease(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireServeLease(t.Context(), st, "second", logger); err != nil {
		t.Fatalf("second controller after release: %v", err)
	}
}

// Transfer URLs a runner holds survive a controller restart.
func TestDataSigningKeyIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-signing.key")
	first, err := dataSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := dataSigningKey(path)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("restart key differs: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v, %v; want mode 0600", info, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dataSigningKey(path); err == nil {
		t.Fatal("a truncated signing key was accepted")
	}
}
