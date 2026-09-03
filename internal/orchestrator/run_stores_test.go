package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func isolateDaemon(t *testing.T) {
	t.Helper()
	t.Setenv("SPARKWING_WINGD_BIN", "")
	t.Setenv("PATH", t.TempDir())
}

func seedRuns(t *testing.T, path string, runs ...store.Run) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = st.Close() }()
	for _, r := range runs {
		if err := st.CreateRun(context.Background(), r); err != nil {
			t.Fatalf("create run %s: %v", r.ID, err)
		}
	}
}

func runAt(id, pipeline, status string, minutesAgo int) store.Run {
	return store.Run{
		ID:        id,
		Pipeline:  pipeline,
		Status:    status,
		StartedAt: time.Now().Add(-time.Duration(minutesAgo) * time.Minute).UTC(),
	}
}

func homeWithBothStores(t *testing.T) orchestrator.Paths {
	t.Helper()
	isolateDaemon(t)
	p := newPaths(t)
	t.Setenv("SPARKWING_HOME", p.Root)
	seedRuns(t, p.StateDB(), runAt("shared-old", "alpha", "success", 30), runAt("shared-new", "alpha", "failed", 5))
	seedRuns(t, p.StandaloneStateDB(), runAt("alone-mid", "beta", "success", 15))
	return p
}

func TestListJobs_MergesStandaloneStores(t *testing.T) {
	p := homeWithBothStores(t)
	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "STORE") {
		t.Fatalf("expected a STORE column once a standalone run lists:\n%s", out)
	}
	order := []string{"shared-new", "alone-mid", "shared-old"}
	at := -1
	for _, id := range order {
		next := strings.Index(out, id)
		if next < 0 {
			t.Fatalf("list is missing %s:\n%s", id, out)
		}
		if next < at {
			t.Fatalf("list is not newest first (%s out of place):\n%s", id, out)
		}
		at = next
	}
	line := lineContaining(t, out, "alone-mid")
	if !strings.Contains(line, "standalone/state.db") {
		t.Fatalf("standalone row is not tagged with its store: %q", line)
	}
	if shared := lineContaining(t, out, "shared-new"); !strings.Contains(shared, "shared") {
		t.Fatalf("shared row is not tagged: %q", shared)
	}
}

func TestListJobs_JSONCarriesTheStore(t *testing.T) {
	p := homeWithBothStores(t)
	var buf bytes.Buffer
	err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10, JSON: true}, &buf)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var row struct {
			ID    string `json:"id"`
			Store string `json:"store"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		got[row.ID] = row.Store
	}
	if got["shared-new"] != "shared" {
		t.Fatalf("shared run tagged %q", got["shared-new"])
	}
	if got["alone-mid"] != "standalone/state.db" {
		t.Fatalf("standalone run tagged %q", got["alone-mid"])
	}
}

func TestListJobs_NoStandaloneDirectoryKeepsTheOldTable(t *testing.T) {
	isolateDaemon(t)
	p := newPaths(t)
	t.Setenv("SPARKWING_HOME", p.Root)
	seedRuns(t, p.StateDB(), runAt("only-shared", "alpha", "success", 5))

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if strings.Contains(buf.String(), "STORE") {
		t.Fatalf("a home with no standalone store grew a STORE column:\n%s", buf.String())
	}

	buf.Reset()
	err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10, JSON: true}, &buf)
	if err != nil {
		t.Fatalf("ListJobs json: %v", err)
	}
	if !strings.Contains(buf.String(), `"store":"shared"`) {
		t.Fatalf("json row is not tagged shared:\n%s", buf.String())
	}
}

func TestJobStatus_FindsAStandaloneRun(t *testing.T) {
	p := homeWithBothStores(t)
	var buf bytes.Buffer
	err := orchestrator.JobStatus(context.Background(), p, "alone-mid", orchestrator.StatusOpts{}, &buf)
	if err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "alone-mid") {
		t.Fatalf("status did not resolve the standalone run:\n%s", out)
	}
	if !strings.Contains(out, "standalone/state.db") {
		t.Fatalf("status did not say where the run lives:\n%s", out)
	}
}

func TestJobStatusJSON_CarriesTheStandaloneStore(t *testing.T) {
	p := homeWithBothStores(t)
	var buf bytes.Buffer
	err := orchestrator.JobStatus(context.Background(), p, "alone-mid", orchestrator.StatusOpts{JSON: true}, &buf)
	if err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	var payload struct {
		Store string `json:"store"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("decode status json: %v", err)
	}
	if payload.Store != "standalone/state.db" {
		t.Fatalf("status json tagged the store %q", payload.Store)
	}
}

func TestRunSummaryLocal_FindsAStandaloneRun(t *testing.T) {
	p := homeWithBothStores(t)
	var buf bytes.Buffer
	err := orchestrator.RunSummaryLocal(context.Background(), p, "alone-mid",
		orchestrator.SummaryOpts{JSON: true}, &buf)
	if err != nil {
		t.Fatalf("RunSummaryLocal: %v", err)
	}
	var got orchestrator.RunSummary
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got.RunID != "alone-mid" || got.Store != "standalone/state.db" {
		t.Fatalf("summary resolved %q from %q", got.RunID, got.Store)
	}
}

func TestOpenStoreForRun_ReadsWithoutWriting(t *testing.T) {
	p := homeWithBothStores(t)
	st, label, done, err := orchestrator.OpenStoreForRun(context.Background(), p, "alone-mid")
	if err != nil {
		t.Fatalf("OpenStoreForRun: %v", err)
	}
	defer done()
	if label != "standalone/state.db" {
		t.Fatalf("labeled the store %q", label)
	}
	var queryOnly int
	if err := st.DB().QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatalf("read query_only: %v", err)
	}
	if queryOnly != 1 {
		t.Fatal("a standalone store was opened for writing")
	}
}

func TestOpenStandaloneStores_UnknownRequirementBecomesANote(t *testing.T) {
	p := homeWithBothStores(t)
	stampRequirement(t, p.StandaloneSchemaDir(99), "moon_phase_index", "v9.9.9")

	stores := orchestrator.OpenStandaloneStores(context.Background(), p)
	defer func() { _ = stores.Close() }()

	notes := stores.Notes()
	if len(notes) != 1 || !strings.Contains(notes[0], "sparkwing >= v9.9.9") {
		t.Fatalf("expected one note naming the release, got %v", notes)
	}
	rows := stores.ListRuns(context.Background(), store.RunFilter{Limit: 10})
	if len(rows) != 1 || rows[0].ID != "alone-mid" {
		t.Fatalf("the readable standalone store stopped listing: %v", rows)
	}
}

func TestStandaloneRunError_NamesTheStoreAndTheHome(t *testing.T) {
	p := homeWithBothStores(t)
	err := orchestrator.StandaloneRunError(context.Background(), p, "alone-mid", "runs cancel")
	if err == nil {
		t.Fatal("expected an error for a run that lives in a standalone store")
	}
	msg := err.Error()
	for _, want := range []string{
		filepath.Join(p.StandaloneDir(), "state.db"),
		"SPARKWING_HOME=" + p.StandaloneDir(),
		"runs cancel",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error does not mention %q: %s", want, msg)
		}
	}
	if err := orchestrator.StandaloneRunError(context.Background(), p, "shared-new", "runs cancel"); err != nil {
		t.Fatalf("a shared run must not answer with the standalone remedy: %v", err)
	}
}

func stampRequirement(t *testing.T, dir, name, addedBy string) {
	t.Helper()
	path := filepath.Join(dir, "state.db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	_, err = st.DB().Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES (?, ?, ?)`,
		name, time.Now().UnixNano(), addedBy)
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close %s: %v", path, cerr)
	}
	if err != nil {
		t.Fatalf("stamp requirement: %v", err)
	}
}

func lineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", needle, out)
	return ""
}
