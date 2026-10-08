package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func TestCIEmbedded_LogStore(t *testing.T) {
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

	p := newPathsWithStore(t)
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

	got, err := logStore.Read(context.Background(), res.RunID, "hello", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("LogStore.Read: %v", err)
	}
	if !strings.Contains(string(got), "hello-from-ci-embedded") {
		t.Errorf("log read = %q, want hello-from-ci-embedded", got)
	}
}

func TestCIEmbedded_LogStore_OverridesLocalLogs(t *testing.T) {
	logStore, err := fs.NewLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}

	p := newPathsWithStore(t)
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
