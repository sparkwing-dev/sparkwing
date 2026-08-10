package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleSocketTreatsAFreshHomeAsSpawnSafe(t *testing.T) {
	home := t.TempDir()
	spawnSafe, err := RemoveStaleSocket(home)
	if err != nil {
		t.Fatal(err)
	}
	if !spawnSafe {
		t.Fatal("fresh home was not spawn-safe")
	}
	if held, err := LockHeld(home); err != nil || held {
		t.Fatalf("fresh-home election after probe = held %v, err %v; want released", held, err)
	}
}

func TestRemoveStaleSocketReportsFreeElectionWhenCleanupFails(t *testing.T) {
	home := t.TempDir()
	sock, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sock, "block"), 0o700); err != nil {
		t.Fatal(err)
	}
	spawnSafe, err := RemoveStaleSocket(home)
	if err == nil {
		t.Fatal("non-removable socket path reported successful cleanup")
	}
	if !spawnSafe {
		t.Fatalf("cleanup error also hid the free election: %v", err)
	}
}
