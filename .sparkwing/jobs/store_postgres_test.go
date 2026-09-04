package jobs

import (
	"path/filepath"
	"strings"
	"testing"
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
