package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// TestEnsureDaemon_SurfacesDaemonBindFailure pins BW-650: when a spawned
// daemon dies at startup because it cannot bind, the returned error carries
// the daemon's own bind failure (via its log) instead of an unrelated
// spawn-layer error.
func TestEnsureDaemon_SurfacesDaemonBindFailure(t *testing.T) {
	home := shortHome(t)
	stateDir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sock, "block"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath, _ := wingd.LogPath(home)

	spawn := func(h, v string) error {
		d, derr := wingd.New(wingd.Config{Home: h, Version: v})
		if derr != nil {
			return derr
		}
		runErr := d.Run(context.Background())
		if runErr != nil {
			if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
				fmt.Fprintf(f, "sparkwing error: %v\n", runErr)
				_ = f.Close()
			}
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = EnsureDaemon(ctx, Options{
		Home:        home,
		Spawn:       spawn,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected EnsureDaemon to fail when the daemon cannot bind")
	}
	if !strings.Contains(err.Error(), "bind") {
		t.Fatalf("error should surface the daemon bind failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), logPath) {
		t.Fatalf("error should name the daemon log path %q, got: %v", logPath, err)
	}
}
