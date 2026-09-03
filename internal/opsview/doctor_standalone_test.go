package opsview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func writeStandaloneRuns(t *testing.T, p paths.Paths, schema int, ids ...string) string {
	t.Helper()
	dir := p.StandaloneSchemaDir(schema)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = st.Close() }()
	for i, id := range ids {
		if err := st.CreateRun(context.Background(), store.Run{
			ID:        id,
			Pipeline:  "p",
			Status:    "success",
			StartedAt: time.Now().Add(-time.Duration(i+1) * 25 * time.Hour),
		}); err != nil {
			t.Fatalf("create run %s: %v", id, err)
		}
	}
	return path
}

func TestDiagnoseStandaloneStores_CountsRunsAndTheOldest(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	schema := store.ExpectedSchemaVersion()
	path := writeStandaloneRuns(t, p, schema, "a", "b")
	if err := os.MkdirAll(filepath.Join(p.StandaloneDir(), "not-a-schema"), 0o700); err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseStandaloneStores(p, &report)

	if len(report.StandaloneStores) != 1 {
		t.Fatalf("standalone stores = %+v, want the one directory holding a store", report.StandaloneStores)
	}
	got := report.StandaloneStores[0]
	if got.Path != path || got.Schema != schema || got.Runs != 2 {
		t.Fatalf("store = %+v, want %s at schema %d with 2 runs", got, path, schema)
	}
	if got.OldestRunAt == nil || time.Since(*got.OldestRunAt) < 48*time.Hour {
		t.Fatalf("oldest run = %v, want the older of the two", got.OldestRunAt)
	}

	var out strings.Builder
	renderStandaloneStores(&out, report)
	for _, want := range []string{"standalone runs stores", path, "2 run(s)", "oldest 2d", "nothing prunes them"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rendered report %q does not contain %q", out.String(), want)
		}
	}
}

func TestDiagnoseStandaloneStores_SaysNothingWithoutOne(t *testing.T) {
	var report DoctorReport
	diagnoseStandaloneStores(paths.PathsAt(t.TempDir()), &report)
	if len(report.StandaloneStores) != 0 {
		t.Fatalf("standalone stores = %+v, want none", report.StandaloneStores)
	}
	if !report.Clean() {
		t.Error("an empty standalone report is not a fault")
	}
	var out strings.Builder
	renderStandaloneStores(&out, report)
	if out.String() != "" {
		t.Errorf("rendered %q for a home with no standalone store", out.String())
	}
}

// safety: doctor is the installed build and its schema outranks a store an
// older pipeline binary still opens, so reading one must not migrate it.
func TestDiagnoseStandaloneStores_LeavesAnOlderSchemaAlone(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	path := writeStandaloneRuns(t, p, store.ExpectedSchemaVersion(), "a")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseStandaloneStores(p, &report)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("reading the standalone store rewrote it")
	}
}
