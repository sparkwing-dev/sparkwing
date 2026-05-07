package orchestrator_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// ciEmbeddedHelloPipe is the smallest pipeline that exercises the
// LogStore + ArtifactStore plumbing: one node that emits one log
// line. We assert the line lands in the LogStore and that the run
// state lands in the ArtifactStore after RunLocal returns.
type ciEmbeddedHelloPipe struct{ sparkwing.Base }

func (ciEmbeddedHelloPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "hello", func(ctx context.Context) error {
		sparkwing.LoggerFromContext(ctx).Log("info", "hello-from-ci-embedded")
		return nil
	})
	return nil
}

func init() {
	register("ci-embedded-hello", func() sparkwing.Pipeline[sparkwing.NoInputs] { return ciEmbeddedHelloPipe{} })
}

func TestCIEmbedded_LogStore_AndStateDump(t *testing.T) {
	t.Parallel()

	logRoot := t.TempDir()
	artRoot := t.TempDir()
	logStore, err := fs.NewLogStore(logRoot)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	artStore, err := fs.NewArtifactStore(artRoot)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:      "ci-embedded-hello",
		LogStore:      logStore,
		ArtifactStore: artStore,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}

	// The log line should be in the LogStore at (runID, hello).
	got, err := logStore.Read(context.Background(), res.RunID, "hello", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("LogStore.Read: %v", err)
	}
	if !strings.Contains(string(got), "hello-from-ci-embedded") {
		t.Errorf("log read = %q, want hello-from-ci-embedded", got)
	}

	// State dump should be at runs/<runID>/state.ndjson.
	rc, err := artStore.Get(context.Background(), "runs/"+res.RunID+"/state.ndjson")
	if err != nil {
		t.Fatalf("ArtifactStore.Get state.ndjson: %v", err)
	}
	defer rc.Close()
	dump, _ := io.ReadAll(rc)
	if len(dump) == 0 {
		t.Fatal("state.ndjson is empty")
	}
	// First line is the run record.
	lines := strings.Split(strings.TrimSpace(string(dump)), "\n")
	if len(lines) < 2 {
		t.Fatalf("state.ndjson should have run + node lines, got %d lines: %q", len(lines), dump)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode line 0: %v", err)
	}
	if first["kind"] != "run" {
		t.Errorf("line 0 kind = %v, want run", first["kind"])
	}
	// Node line should reference our node id.
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("decode line 1: %v", err)
	}
	if second["kind"] != "node" {
		t.Errorf("line 1 kind = %v, want node", second["kind"])
	}
}

func TestCIEmbedded_LogStore_OverridesLocalLogs(t *testing.T) {
	// When LogStore is set, the local on-disk LogBackend is bypassed.
	// The dispatcher's log lines never appear under paths.RunDir().
	t.Parallel()

	logStore, err := fs.NewLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "ci-embedded-hello",
		LogStore: logStore,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}
	got, _ := logStore.Read(context.Background(), res.RunID, "hello", storage.ReadOpts{})
	if !strings.Contains(string(got), "hello-from-ci-embedded") {
		t.Errorf("LogStore read = %q", got)
	}
}
