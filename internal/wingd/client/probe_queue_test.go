package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestProbeQueueDoesNotInitializeAbsentDaemonState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	socket, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProbeQueue(context.Background(), socket); !errors.Is(err, client.ErrNoDaemon) {
		t.Fatalf("ProbeQueue error = %v, want ErrNoDaemon", err)
	}
	stateDir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ProbeQueue created daemon state: %v", err)
	}
}
