package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDaemonSocketTreatsAFreshHomeAsReady(t *testing.T) {
	home := t.TempDir()
	state, err := PrepareDaemonSocket(home)
	if err != nil {
		t.Fatal(err)
	}
	if state != SocketPreparationReady {
		t.Fatalf("fresh home preparation = %v, want ready", state)
	}
	if held, err := LockHeld(home); err != nil || held {
		t.Fatalf("fresh-home election after probe = held %v, err %v; want released", held, err)
	}
}

func TestPrepareDaemonSocketDistinguishesCleanupFailure(t *testing.T) {
	home := t.TempDir()
	sock, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sock, "block"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := PrepareDaemonSocket(home)
	if err == nil {
		t.Fatal("non-removable socket path reported successful cleanup")
	}
	if state != SocketPreparationCleanupFailed {
		t.Fatalf("cleanup error preparation = %v, want cleanup-failed: %v", state, err)
	}
}
