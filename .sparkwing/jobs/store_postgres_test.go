package jobs

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
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
