package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The declared type passes the plan-time check -- an `any` field can
// hold anything -- and the value in it cannot be encoded. That gap is
// exactly why the execution-time check exists alongside the plan-time
// one.
type smuggledPayload struct {
	Name string `json:"name"`
	Data any    `json:"data"`
}

type smugglerJob struct {
	sparkwing.Base
	sparkwing.Produces[smuggledPayload]
}

func (j *smugglerJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (smuggledPayload, error) {
		return smuggledPayload{Name: "n", Data: make(chan int)}, nil
	}), nil
}

type smugglerPipe struct{ sparkwing.Base }

func (smugglerPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "produce", &smugglerJob{})
	return nil
}

var smugglerOnce sync.Once

func registerSmugglerPipe() {
	smugglerOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("output-smuggler",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &smugglerPipe{} })
	})
}

// A node whose output will not encode has produced nothing a
// downstream node can read. Reporting success drops the output on the
// floor and hands every consumer an empty value, so the node fails and
// says why.
func TestRun_UnencodableNodeOutputFailsTheNode(t *testing.T) {
	registerSmugglerPipe()
	p := newPaths(t)

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "output-smuggler",
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	n, err := st.GetNode(context.Background(), res.RunID, "produce")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", n.Outcome)
	}
	for _, want := range []string{"JSON", "docs/migrations/v0.36.0.md#process-per-node"} {
		if !strings.Contains(n.Error, want) {
			t.Errorf("node error %q does not mention %q", n.Error, want)
		}
	}
}
