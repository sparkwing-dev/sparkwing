package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
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

func TestStoreFilesKeepOnlyTheStorePackage(t *testing.T) {
	got := storeFiles([]string{
		"internal/store/store.go",
		"internal/store/storetest/storetest.go",
		"internal/store/testdata/v28.sql",
		"internal/storage/s3state/s3state.go",
		"internal/orchestrator/orchestrator.go",
	})
	want := []string{"internal/store/store.go", "internal/store/storetest/storetest.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storeFiles = %v, want %v", got, want)
	}
}

func TestStorePostgresStepPassesWithoutPostgresWhenTheStoreIsUntouched(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "internal", "other.go"), "package internal\n\nfunc Other() int { return 2 }\n")
	gitAddAll(t, root)
	t.Setenv("SPARKWING_TEST_PG_URL", "postgres://nobody@127.0.0.1:1/unreachable")

	if err := runStorePostgresIfTouched(context.Background(), gateAffected); err != nil {
		t.Fatalf("store-postgres ran or failed with internal/store untouched: %v", err)
	}
}

func TestStorePostgresStepWaitsOnTheTestStep(t *testing.T) {
	w := inspectWork(t, &Gate{})
	if findStep(w, "store-postgres") == nil {
		t.Fatal("pre-commit does not run store-postgres")
	}
	if !stepWaitsOn(w, "store-postgres", "test") {
		t.Error("store-postgres does not wait on test")
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

func TestStorePostgresShardsFollowTheGoStepBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host hostShape
		want int
	}{
		{hostShape{cpus: 4, singleTenant: true}, 4},
		{hostShape{cpus: 4}, 1},
		{hostShape{cpus: 8, singleTenant: true}, 4},
		{hostShape{cpus: 16}, 7},
		{hostShape{cpus: 2}, 1},
	} {
		if got := storePostgresShardCount(tc.host); got != tc.want {
			t.Errorf("storePostgresShardCount(%+v) = %d, want %d", tc.host, got, tc.want)
		}
	}
}

func TestStorePostgresShardDatabaseKeepsTheServerAndOptions(t *testing.T) {
	t.Parallel()
	got, err := storePostgresDatabaseURL("postgres://sw:sw@127.0.0.1:55432/sw?sslmode=disable", "sparkwing_store_shard_ab_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgres://sw:sw@127.0.0.1:55432/sparkwing_store_shard_ab_1?sslmode=disable"; got != want {
		t.Fatalf("shard URL = %q, want %q", got, want)
	}
	for base, want := range map[string]string{
		"postgres://sw:sw@127.0.0.1:55432/sw?dbname=shared&sslmode=disable": "postgres://sw:sw@127.0.0.1:55432/sparkwing_store_shard_ab_1?sslmode=disable",
		"postgresql://sw@127.0.0.1/?database=shared":                        "postgresql://sw@127.0.0.1/sparkwing_store_shard_ab_1",
	} {
		got, err := storePostgresDatabaseURL(base, "sparkwing_store_shard_ab_1")
		if err != nil || got != want {
			t.Errorf("shard URL from %q = %q, %v, want %q without the overriding option", base, got, err, want)
		}
	}
	for _, dsn := range []string{"host=localhost user=sw dbname=sw", "mysql://localhost/sw", "postgres://localhost/sw?service=ci"} {
		if _, err := storePostgresDatabaseURL(dsn, "x"); err == nil {
			t.Errorf("accepted %q, which names no postgres:// URL", dsn)
		}
	}
}

func TestStorePostgresShardTimeoutEndsInsideTheStep(t *testing.T) {
	t.Parallel()
	args := storePostgresShardArgs([]string{"TestA"})
	i := slices.IndexFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "-test.timeout=") })
	if i < 0 {
		t.Fatalf("shard args %q set no timeout", args)
	}
	timeout, err := time.ParseDuration(strings.TrimPrefix(args[i], "-test.timeout="))
	if err != nil {
		t.Fatal(err)
	}
	if timeout >= storePostgresPrePushTimeout {
		t.Fatalf("shard timeout %s does not end before the step's %s, so a hung test would print no stack", timeout, storePostgresPrePushTimeout)
	}
}

func TestStorePostgresSweepRemovesOnlyAbandonedRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no flock outside unix")
	}
	dir := t.TempDir()
	mkRoot := func(name string) string {
		root := filepath.Join(dir, storePostgresRootPrefix+name)
		if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
		return root
	}
	live := mkRoot("live")
	release, err := claimStorePostgresRoot(live)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	abandoned := mkRoot("abandoned")
	releaseAbandoned, err := claimStorePostgresRoot(abandoned)
	if err != nil {
		t.Fatal(err)
	}
	releaseAbandoned()
	unclaimed := mkRoot("unclaimed")

	sweepAbandonedStorePostgresRoots(t.Context(), dir)

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("the root whose owner is gone survived the sweep: %v", err)
	}
	for _, root := range []string{live, unclaimed} {
		if _, err := os.Stat(root); err != nil {
			t.Errorf("the sweep removed %s, which a live run may still own: %v", root, err)
		}
	}
}

func TestStorePostgresRootRemovalDeletesTheOwnerLockLast(t *testing.T) {
	root := filepath.Join(t.TempDir(), storePostgresRootPrefix+"x")
	locked := filepath.Join(root, "data", "base")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "relation"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, storePostgresOwnerLock), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := removeStorePostgresRoot(root); err == nil {
		t.Skip("running as a user that ignores directory permissions")
	}
	if _, err := os.Stat(filepath.Join(root, storePostgresOwnerLock)); err != nil {
		t.Errorf("a failed data removal still dropped the owner lock: %v", err)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeStorePostgresRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("root survived removal: %v", err)
	}
}
