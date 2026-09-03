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
	dir := p.StandaloneDir()
	if schema > 0 {
		dir = p.StandaloneSchemaDir(schema)
	}
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

func TestDiagnoseStandaloneStores_ListsTheSharedStoreAndEveryFallback(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	shared := writeStandaloneRuns(t, p, 0, "a", "b")
	own := writeStandaloneRuns(t, p, 26, "c")
	if err := os.MkdirAll(filepath.Join(p.StandaloneDir(), "not-a-schema"), 0o700); err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseStandaloneStores(p, &report)

	if len(report.StandaloneStores) != 2 {
		t.Fatalf("standalone stores = %+v, want the shared store and the one fallback", report.StandaloneStores)
	}
	got := report.StandaloneStores[0]
	if got.Path != shared || got.Schema != 0 || got.Runs != 2 {
		t.Fatalf("shared store = %+v, want %s with 2 runs and no schema of its own", got, shared)
	}
	if got.OldestRunAt == nil || time.Since(*got.OldestRunAt) < 48*time.Hour {
		t.Fatalf("oldest run = %v, want the older of the two", got.OldestRunAt)
	}
	if fallback := report.StandaloneStores[1]; fallback.Path != own || fallback.Schema != 26 || fallback.Runs != 1 {
		t.Fatalf("fallback = %+v, want %s at schema 26 with 1 run", fallback, own)
	}

	var out strings.Builder
	renderStandaloneStores(&out, report)
	for _, want := range []string{"standalone runs stores", shared, own, "2 run(s)", "oldest 2d", "nothing prunes them"} {
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

// safety: doctor is the installed build and its schema may outrank a store an
// older pipeline binary still opens, so reading one must not migrate it.
func TestDiagnoseStandaloneStores_LeavesAStoreAtAnotherSchemaAlone(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	path := writeStandaloneRuns(t, p, store.ExpectedSchemaVersion()-1, "a")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseStandaloneStores(p, &report)
	if len(report.StandaloneStores) != 1 || report.StandaloneStores[0].Schema != store.ExpectedSchemaVersion()-1 {
		t.Fatalf("stores = %+v, want the neighboring-schema directory listed", report.StandaloneStores)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("reading the standalone store rewrote it")
	}
}

// safety: a store doctor cannot read is a thing to report, not a directory to
// drop from the listing as though no runs went there.
func TestDiagnoseStandaloneStores_ReportsAnUnreadableStore(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	if err := os.MkdirAll(p.StandaloneDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.StandaloneDir(), "state.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	var report DoctorReport
	diagnoseStandaloneStores(p, &report)
	if len(report.StandaloneStores) != 1 || report.StandaloneStores[0].Runs >= 0 {
		t.Fatalf("stores = %+v, want the unreadable file reported", report.StandaloneStores)
	}
	var out strings.Builder
	renderStandaloneStores(&out, report)
	if !strings.Contains(out.String(), "unreadable") {
		t.Errorf("rendered report %q does not say the store could not be read", out.String())
	}
}
