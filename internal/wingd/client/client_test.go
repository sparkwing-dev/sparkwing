package client

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestVersionNewer(t *testing.T) {
	tests := []struct {
		client, daemon string
		want           bool
	}{
		{"v2.0.0", "v1.0.0", true},
		{"v1.2.0", "v1.1.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v2.0.0", false},
		{"2.0.0", "1.0.0", true},
		{"", "v1.0.0", false},
		{"v1.0.0", "", false},
		{"garbage", "v1.0.0", false},
	}
	for _, tt := range tests {
		if got := versionNewer(tt.client, tt.daemon); got != tt.want {
			t.Errorf("versionNewer(%q,%q)=%v, want %v", tt.client, tt.daemon, got, tt.want)
		}
	}
}

func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wdcl")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// spawnInProcess returns a Spawn hook that brings up a real daemon inside
// the test process the first time it fires, so EnsureDaemon exercises its
// spawn-and-retry path without a child process.
func spawnInProcess(t *testing.T, home string) func(string, string) error {
	var once sync.Once
	return func(string, string) error {
		once.Do(func() {
			d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0"})
			if err != nil {
				t.Errorf("spawn: new daemon: %v", err)
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { _ = d.Run(ctx) }()
		})
		return nil
	}
}

func TestEnsureDaemon_SpawnsWhenAbsent(t *testing.T) {
	home := shortHome(t)
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       spawnInProcess(t, home),
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()
	if cl.DaemonVersion() != "v1.0.0" {
		t.Fatalf("daemon version %q, want v1.0.0", cl.DaemonVersion())
	}
	lease, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:     "r1",
		Resources: wingwire.HostResources{Cores: 0.5},
	}, nil)
	if err != nil {
		t.Fatalf("acquire against spawned daemon: %v", err)
	}
	if lease.RunID != "r1" {
		t.Fatalf("lease run id %q, want r1", lease.RunID)
	}
}

func TestQuery_NoDaemonReturnsSentinel(t *testing.T) {
	home := shortHome(t)
	_, err := Query(context.Background(), Options{
		Home:        home,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("Query with no daemon: got %v, want ErrNoDaemon", err)
	}
}
