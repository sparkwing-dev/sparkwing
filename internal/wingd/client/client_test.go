package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestSupersedes(t *testing.T) {
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
		{"(devel)", "v1.0.0", false},
		{"(devel)", "(devel)", false},
		{"(devel)", "", false},
		{"(unknown)", "v1.0.0", false},
		{"v1.0.0+dirty", "v1.0.0", false},
		{"v1.0.0+dirty", "v1.0.0+dirty", false},
		{"v0.9.0+dirty", "v1.0.0", false},
		{"v1.0.0", "(devel)", false},
		{"v1.0.0", "v1.0.0+dirty", false},
		{"v1.1.0", "v1.0.0+dirty", false},
		{"v0.22.2", "v0.22.2-dev+e99c1800", false},
		{"v0.22.2-dev+e99c1800", "v0.22.2", true},
		{"v0.22.2-dev+e99c1800", "v0.22.1", true},
		{"v0.22.2-dev+e99c1800", "v0.22.3", false},
		{"v0.22.3", "v0.22.2-dev+e99c1800", true},
		{"v0.22.2-dev+e99c1800", "v0.22.2-0.20260801181107-3e089db19798", true},
		{"v0.22.2-0.20260801181107-3e089db19798", "v0.22.2-dev+e99c1800", false},
		{"v0.22.2-dev+nothex", "v0.22.2", false},
		{"v0.22.3-dev+22222222", "v0.22.2-dev+11111111", true},
		{"v0.23.0", "v0.22.1-0.20260724005950-041d1c11f150+dirty", false},
		{"(devel)", "v1.0.0+dirty", false},
		{"v1.0.0+dirty", "(devel)", false},
		{"v0.23.0+dirty", "v0.22.0+dirty", true},
		{"v0.22.0+dirty", "v0.23.0+dirty", false},
		{"v0.22.1-0.20260724005950-041d1c11f150+dirty", "v0.22.1-0.20260723005950-041d1c11f150+dirty", true},
	}
	for _, tt := range tests {
		if got := supersedes(tt.client, tt.daemon); got != tt.want {
			t.Errorf("supersedes(%q,%q)=%v, want %v", tt.client, tt.daemon, got, tt.want)
		}
	}
}

// TestSupersedes_NeverMutual pins the property the takeover loop depends
// on: no two builds may each supersede the other. A mutual pair makes two
// concurrently running sparkwings drain and respawn each other's daemon
// without end, since connect() re-takes-over on every reconnect and
// nothing bounds the exchange.
func TestSupersedes_NeverMutual(t *testing.T) {
	versions := []string{
		"", "(devel)", "(unknown)", "garbage",
		"v1.0.0", "v1.1.0", "v2.0.0",
		"v0.22.0", "v0.23.0",
		"v1.0.0+dirty", "v0.22.0+dirty",
		"v0.22.2-dev+e99c1800", "v0.22.3-dev+22222222",
		"v0.22.1-0.20260724005950-041d1c11f150+dirty",
		"v0.22.1-0.20260724005950-041d1c11f150",
	}
	for _, a := range versions {
		for _, b := range versions {
			if supersedes(a, b) && supersedes(b, a) {
				t.Errorf("%q and %q supersede each other; a live pair of these builds would drain each other's daemon forever", a, b)
			}
		}
	}
}

func TestAdmissionError_FillsVersionSkewOnOpaqueInvalid(t *testing.T) {
	cl := &Client{opts: Options{Version: "(devel)"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail})
	if e.Reason == "" {
		t.Fatal("opaque invalid rejection under version skew left no reason")
	}
	if !strings.Contains(e.Reason, "v0.18.0") || !strings.Contains(e.Reason, "(devel)") {
		t.Fatalf("skew reason %q does not name both versions", e.Reason)
	}
}

func TestAdmissionError_KeepsDaemonReasonWhenGiven(t *testing.T) {
	cl := &Client{opts: Options{Version: "(devel)"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail, Reason: "admission request invalid: named cause"})
	if e.Reason != "admission request invalid: named cause" {
		t.Fatalf("daemon-provided reason was overwritten: %q", e.Reason)
	}
}

func TestAdmissionError_NoSkewHintWhenVersionsMatch(t *testing.T) {
	cl := &Client{opts: Options{Version: "v0.18.0"}, ack: wingwire.HelloAck{BinaryVersion: "v0.18.0"}}
	e := cl.admissionError(&wingwire.Evicted{Key: "invalid", Policy: wingwire.PolicyFail})
	if e.Reason != "" {
		t.Fatalf("matched versions produced a skew hint: %q", e.Reason)
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

func TestEnsureDaemon_BoundsAnUnreachablePredecessorElection(t *testing.T) {
	home := shortHome(t)
	_ = runDaemon(t, home, "v1.0.0")
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sock); err != nil {
		t.Fatalf("hide predecessor listener: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = EnsureDaemon(ctx, Options{
		Home:        home,
		Spawn:       func(string, string) error { t.Fatal("spawned while predecessor held election"); return nil },
		DialTimeout: time.Millisecond,
		Backoff:     time.Nanosecond,
	})
	if err == nil {
		t.Fatal("unreachable predecessor election waited forever")
	}
	if !errors.Is(err, ErrDaemonUnreachable) || !strings.Contains(err.Error(), "predecessor") || !strings.Contains(err.Error(), "election lock") {
		t.Fatalf("unreachable predecessor error = %v; want bounded election-lock diagnostic", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("predecessor wait consumed the caller deadline: %v", err)
	}
}

func TestEnsureDaemon_WaitsForOneSlowHealthySpawn(t *testing.T) {
	home := shortHome(t)
	var calls atomic.Int32
	spawn := func(string, string) error {
		calls.Add(1)
		go func() {
			time.Sleep(750 * time.Millisecond)
			d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0"})
			if err != nil {
				t.Errorf("spawn: new daemon: %v", err)
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			_ = d.Run(ctx)
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cl, err := EnsureDaemon(ctx, Options{
		Home:        home,
		Spawn:       spawn,
		DialTimeout: 10 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure slow-starting daemon: %v", err)
	}
	defer cl.Close()
	if got := calls.Load(); got != 1 {
		t.Fatalf("spawn calls = %d, want one startup attempt", got)
	}
}

// TestEnsureDaemon_DevClientJoinsReleaseDaemon verifies that dev builds
// connect to a release daemon without triggering a takeover. Two concurrent
// dev clients must both succeed and see the release daemon version, not a
// spawned replacement.
func TestEnsureDaemon_DevClientJoinsReleaseDaemon(t *testing.T) {
	home := shortHome(t)
	spawn := spawnInProcess(t, home)

	connect := func(id string) (string, error) {
		cl, err := EnsureDaemon(context.Background(), Options{
			Home:        home,
			Version:     "(devel)",
			Spawn:       spawn,
			DialTimeout: 500 * time.Millisecond,
			Backoff:     20 * time.Millisecond,
		})
		if err != nil {
			return "", err
		}
		defer cl.Close()
		ver := cl.DaemonVersion()
		_, err = cl.Acquire(context.Background(), wingwire.AdmissionRequest{
			RunID:     id,
			Resources: wingwire.HostResources{Cores: 0.5},
		}, nil)
		return ver, err
	}

	type result struct {
		ver string
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i, id := range []string{"dev-run-a", "dev-run-b"} {
		wg.Add(1)
		go func(idx int, runID string) {
			defer wg.Done()
			ver, err := connect(runID)
			results[idx] = result{ver, err}
		}(i, id)
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Errorf("client %d: %v", i, r.err)
		}
		if r.ver != "v1.0.0" {
			t.Errorf("client %d: daemon version %q after dev connect, want v1.0.0 (takeover must not have fired)", i, r.ver)
		}
	}
}

func TestEnsureDaemon_DevClientLogsWhenJoiningReleaseDaemon(t *testing.T) {
	home := shortHome(t)
	spawn := func(string, string) error {
		d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0"})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = d.Run(ctx) }()
		return nil
	}

	var logged []string
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "(devel)",
		Spawn:       spawn,
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
		Logf:        func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	cl.Close()
	found := false
	for _, msg := range logged {
		if strings.Contains(msg, "v1.0.0") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a log message naming the release daemon version; got %v", logged)
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
