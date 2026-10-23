package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type pipelineDocument struct {
	Jobs           []jobDocument
	Priority       int
	AdmissionClass sparkwing.AdmissionClass `json:"admission_class"`
}

type jobDocument struct {
	ID              string
	Needs           []string `json:"deps"`
	Steps           []stepDocument
	TimeoutMS       int64 `json:"timeout_ms"`
	Requires        []string
	Inline          bool
	ContinueOnError bool `json:"continue_on_error"`
	Optional        bool
	FailurePolicy   sparkwing.ParallelFailurePolicy `json:"failure_policy"`
	Concurrency     []struct {
		Group    string
		Scope    sparkwing.Scope
		OnLimit  sparkwing.OnLimit `json:"on_limit"`
		Capacity int
	}
}

type stepDocument struct {
	ID                string
	Needs             []string
	Finally           bool
	ContinueOnError   bool `json:"continue_on_error"`
	SafeWithoutDryRun bool `json:"safe_without_dry_run"`
}

type testPipeline struct {
	sparkwing.Base
	build func(context.Context, *sparkwing.Plan) error
}

func (p testPipeline) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return p.build(ctx, plan)
}

type testJob func(*sparkwing.Work) (*sparkwing.WorkStep, error)

func (j testJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) { return j(work) }

var testPipelineNumber atomic.Uint64

func newTestHost(t *testing.T, opts sparkwingtest.Options) sparkwingtest.Host {
	t.Helper()
	host := sparkwingtest.New(opts)
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	return host
}

func decodePipeline(t *testing.T, value any) pipelineDocument {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var doc pipelineDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func inspectPipeline(t *testing.T, name string, args map[string]string) pipelineDocument {
	t.Helper()
	doc, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), name, args)
	if err != nil {
		t.Fatal(err)
	}
	return decodePipeline(t, doc)
}

func preparePlan(t *testing.T, build func(context.Context, *sparkwing.Plan) error) (sparkwingtest.Host, *sparkwing.Plan, pipelineDocument) {
	t.Helper()
	name := fmt.Sprintf("test-plan-%d", testPipelineNumber.Add(1))
	var captured *sparkwing.Plan
	sparkwing.Register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return testPipeline{build: func(ctx context.Context, plan *sparkwing.Plan) error {
			captured = plan
			return build(ctx, plan)
		}}
	})
	host := newTestHost(t, sparkwingtest.Options{})
	doc, err := host.Plan(t.Context(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return host, captured, decodePipeline(t, doc)
}

func inspectPlan(t *testing.T, build func(*sparkwing.Plan) error) pipelineDocument {
	t.Helper()
	_, _, doc := preparePlan(t, func(_ context.Context, plan *sparkwing.Plan) error { return build(plan) })
	return doc
}

func prepareWork(t *testing.T, work sparkwing.Workable) (sparkwingtest.Host, *sparkwing.Plan, jobDocument) {
	t.Helper()
	host, plan, doc := preparePlan(t, func(_ context.Context, plan *sparkwing.Plan) error {
		sparkwing.Job(plan, "job", work)
		return nil
	})
	return host, plan, doc.Jobs[0]
}

func inspectWork(t *testing.T, work sparkwing.Workable) jobDocument {
	t.Helper()
	_, _, doc := prepareWork(t, work)
	return doc
}

func findStep(work jobDocument, id string) *stepDocument {
	for _, step := range work.Steps {
		if step.ID == id {
			return &step
		}
	}
	return nil
}
