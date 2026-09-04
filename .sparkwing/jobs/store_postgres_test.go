package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
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

func TestStoreFilesKeepOnlyTheStorePackage(t *testing.T) {
	got := storeFiles([]string{
		"pkg/store/store.go",
		"pkg/store/storetest/storetest.go",
		"pkg/store/testdata/v28.sql",
		"pkg/storage/s3state/s3state.go",
		"internal/orchestrator/orchestrator.go",
	})
	want := []string{"pkg/store/store.go", "pkg/store/storetest/storetest.go"}
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

	if err := runStorePostgresIfTouched(context.Background()); err != nil {
		t.Fatalf("store-postgres ran or failed with pkg/store untouched: %v", err)
	}
}

func TestStorePostgresStepWaitsOnTheTestStep(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PreCommit{}).Work(w); err != nil {
		t.Fatal(err)
	}
	if w.StepByID("store-postgres") == nil {
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
