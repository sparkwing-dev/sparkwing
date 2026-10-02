package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type producingJob struct {
	sparkwing.Base
	sparkwing.Produces[producedOut]
}

type producedOut struct {
	Tag string `json:"tag"`
}

func (producingJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "produce", func(context.Context) (producedOut, error) {
		return producedOut{Tag: "v1"}, nil
	}), nil
}

func TestNodeExecutorFailsANodeWhoseOutputWasNotStored(t *testing.T) {
	for _, tc := range []struct {
		name      string
		outputDir bool
		want      string
	}{
		{"stored", true, string(sparkwing.Success)},
		{"refused", false, string(sparkwing.Failed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			if !tc.outputDir {
				st.SetOutputDir("")
			}
			ctx := context.Background()
			if err := st.CreateRun(ctx, store.Run{ID: "run-out", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "run-out", NodeID: "multi", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			node := sparkwing.Job(sparkwing.NewPlan(), "multi", producingJob{})
			_, err = NewNodeExecutor(LocalBackends(paths, st, nil)).executeNodeInProcess(ctx, "run-out", node, nil)
			if tc.outputDir && err != nil {
				t.Fatalf("execute with an output dir: %v", err)
			}
			if !tc.outputDir && !errors.Is(err, store.ErrOutputNotStored) {
				t.Fatalf("execute without an output dir: err = %v, want the output refusal", err)
			}
			stored, err := st.GetNode(ctx, "run-out", "multi")
			if err != nil {
				t.Fatal(err)
			}
			if stored.Outcome != tc.want {
				t.Fatalf("outcome = %q (error %q), want %q", stored.Outcome, stored.Error, tc.want)
			}
			if !tc.outputDir && !strings.Contains(stored.Error, "not stored") {
				t.Fatalf("error = %q, want the refusal named", stored.Error)
			}
		})
	}
}

func TestLocalOutputsReady_RefusesAStoreWithNoOutputDir(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := localOutputsReady(st); err != nil {
		t.Fatalf("a sqlite store beside its output dir: %v", err)
	}
	st.SetOutputDir("")
	if err := localOutputsReady(st); !errors.Is(err, ErrSharedStateOutputs) {
		t.Fatalf("a store with no output dir: err = %v, want the shared-state refusal", err)
	}
}
