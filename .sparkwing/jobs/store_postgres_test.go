package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStorePostgresLayoutKeepsDataOutOfTheToolCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := t.TempDir()

	layout := storePostgresLayout(root, cache, 54329)

	if got, want := layout.Runtime, filepath.Join(root, "runtime"); got != want {
		t.Errorf("Runtime = %q, want %q", got, want)
	}
	if got, want := layout.Data, filepath.Join(root, "data"); got != want {
		t.Errorf("Data = %q, want %q", got, want)
	}
	if layout.Binaries != cache {
		t.Errorf("Binaries = %q, want the tool cache %q", layout.Binaries, cache)
	}
	if strings.HasPrefix(layout.Data, cache) {
		t.Errorf("Data %q sits under the persistent tool cache %q", layout.Data, cache)
	}
	if got, want := layout.DSN, "postgres://postgres:postgres@localhost:54329/postgres?sslmode=disable"; got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
}

func TestLastLinesKeepsTheTail(t *testing.T) {
	t.Parallel()
	if got, want := lastLines("a\nb\nc\nd\n", 2), "c\nd"; got != want {
		t.Fatalf("lastLines = %q, want %q", got, want)
	}
	if got, want := lastLines("only\n", 5), "only"; got != want {
		t.Fatalf("lastLines = %q, want %q", got, want)
	}
}

func TestFreeLocalPortReturnsAnUnprivilegedPort(t *testing.T) {
	t.Parallel()
	port, err := freeLocalPort()
	if err != nil {
		t.Fatalf("freeLocalPort: %v", err)
	}
	if port < 1024 {
		t.Fatalf("port = %d, want an unprivileged port", port)
	}
	other, err := freeLocalPort()
	if err != nil {
		t.Fatalf("freeLocalPort (second): %v", err)
	}
	if other == port {
		t.Fatalf("two reservations returned the same port %d", port)
	}
}

func TestStorePostgresCommandBoundsParallelismLikeTheTestPipeline(t *testing.T) {
	t.Parallel()
	if got, want := storePostgresGoCommand(14), "GOMAXPROCS=6 go test -p 6 -count=1 ./pkg/store/..."; got != want {
		t.Fatalf("store-postgres go command = %q, want %q", got, want)
	}
}

func TestStorePostgresRunStopsAndRemovesWhenCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var stopped, removed atomic.Bool
	running := make(chan struct{})
	go func() {
		<-running
		cancel()
	}()

	err := runStorePostgresSuite(ctx, storePostgresRun{
		start:  func() (string, error) { return "postgres://unused", nil },
		stop:   func() error { stopped.Store(true); return nil },
		remove: func() error { removed.Store(true); return nil },
		suite: func(ctx context.Context, _ string) error {
			close(running)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !stopped.Load() {
		t.Error("the server was left running after the run was cancelled")
	}
	if !removed.Load() {
		t.Error("the data directory survived a cancelled run")
	}
	cancel()
}

func TestStorePostgresRunStopsAndRemovesOnInterrupt(t *testing.T) {
	t.Parallel()
	var stopped, removed atomic.Bool
	interrupts := make(chan os.Signal, 1)
	running := make(chan struct{})
	go func() {
		<-running
		interrupts <- os.Interrupt
	}()

	err := runStorePostgresSuite(context.Background(), storePostgresRun{
		interrupts: interrupts,
		start:      func() (string, error) { return "postgres://unused", nil },
		stop:       func() error { stopped.Store(true); return nil },
		remove:     func() error { removed.Store(true); return nil },
		suite: func(context.Context, string) error {
			close(running)
			select {}
		},
	})

	if err == nil || !strings.Contains(err.Error(), "interrupted by") {
		t.Fatalf("err = %v, want the interrupt", err)
	}
	if !stopped.Load() {
		t.Error("the server was left running after the interrupt")
	}
	if !removed.Load() {
		t.Error("the data directory survived the interrupt")
	}
}

func TestStorePostgresRunReportsTheLogAfterStopping(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	log.WriteString("startup banner\n")
	var reported string

	err := runStorePostgresSuite(context.Background(), storePostgresRun{
		start: func() (string, error) { return "postgres://unused", nil },
		stop: func() error {
			log.WriteString("what the server logged during the suite\n")
			return nil
		},
		remove:    func() error { return nil },
		suite:     func(context.Context, string) error { return errors.New("suite failed") },
		serverLog: func() string { return log.String() },
		report:    func(tail string) { reported = tail },
	})

	if err == nil || !strings.Contains(err.Error(), "suite failed") {
		t.Fatalf("err = %v, want the suite failure", err)
	}
	if !strings.Contains(reported, "what the server logged during the suite") {
		t.Fatalf("reported tail = %q, want the lines the server flushed on stop", reported)
	}
}

func TestStorePostgresRunKeepsRemovingWhenTheServerWillNotStart(t *testing.T) {
	t.Parallel()
	var removed atomic.Bool

	err := runStorePostgresSuite(context.Background(), storePostgresRun{
		start:  func() (string, error) { return "", errors.New("no port") },
		stop:   func() error { t.Error("stop ran for a server that never started"); return nil },
		remove: func() error { removed.Store(true); return nil },
		suite:  func(context.Context, string) error { t.Error("the suite ran without a server"); return nil },
	})

	if err == nil || !strings.Contains(err.Error(), "no port") {
		t.Fatalf("err = %v, want the start failure", err)
	}
	if !removed.Load() {
		t.Error("a failed start left the data directory behind")
	}
}

func TestWithTempDirRestoresTheCallersSetting(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", "/original")
	var seen string

	if err := withTempDir(scratch, func() error { seen = os.TempDir(); return nil }); err != nil {
		t.Fatalf("withTempDir: %v", err)
	}

	if seen != scratch {
		t.Errorf("TMPDIR inside the call = %q, want %q", seen, scratch)
	}
	if got := os.Getenv("TMPDIR"); got != "/original" {
		t.Errorf("TMPDIR after the call = %q, want /original", got)
	}
}

func TestSweepStorePostgresRootsSparesLiveAndRecentRuns(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	now := time.Now()
	old := now.Add(-time.Hour)

	mkroot := func(name string, modified time.Time, pid string) string {
		t.Helper()
		root := filepath.Join(tempDir, name)
		if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		if pid != "" {
			if err := os.WriteFile(filepath.Join(root, "data", "postmaster.pid"), []byte(pid+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(root, modified, modified); err != nil {
			t.Fatal(err)
		}
		return root
	}

	abandoned := mkroot(storePostgresRootPrefix+"abandoned", old, "4242")
	stopped := mkroot(storePostgresRootPrefix+"stopped", old, "")
	live := mkroot(storePostgresRootPrefix+"live", old, "99")
	recent := mkroot(storePostgresRootPrefix+"recent", now, "")
	other := mkroot("someone-elses-temp-dir", old, "")

	removed := sweepStorePostgresRoots(tempDir, now, func(pid int) bool { return pid == 99 })

	if len(removed) != 2 {
		t.Fatalf("removed = %v, want the abandoned and the stopped root", removed)
	}
	for _, root := range []string{abandoned, stopped} {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", root)
		}
	}
	for _, root := range []string{live, recent, other} {
		if _, err := os.Stat(root); err != nil {
			t.Errorf("the sweep took %s: %v", root, err)
		}
	}
}
