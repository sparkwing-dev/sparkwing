//go:build windows

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeQueueReportsNoDaemonWhenSocketDirectoryIsAbsentOnWindows(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "sw-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	sock := filepath.Join(dir, "missing", "d.sock")
	if _, err := ProbeQueue(context.Background(), sock); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("queue probe with missing socket directory = %v, want ErrNoDaemon", err)
	}
}

func TestQueryReportsNoDaemonForFreshWindowsHome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Query(ctx, Options{Home: t.TempDir()}); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("queue query without a daemon = %v, want ErrNoDaemon", err)
	}
}
