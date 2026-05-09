package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Every test in this file exercises the post-Plan() ref validation
// pass. Validation runs once per Registration.Invoke, so each test
// registers its own pipeline and triggers Invoke.

// expectPanic captures a panic message and runs assertions against
// it. Fatal if the body returns without panicking.
func expectPanic(t *testing.T, body func(), assert func(msg string)) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic, got none")
		}
		msg, _ := r.(string)
		if msg == "" {
			t.Fatalf("panic value not a string: %T %v", r, r)
		}
		assert(msg)
	}()
	body()
}

// --- Work-level (WorkStep.Needs) ---

type typoCloseJob struct{ sparkwing.Base }

func (typoCloseJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs("fetchh")
	return nil, nil
}

type typoCloseJobPipe struct{ sparkwing.Base }

func (typoCloseJobPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", typoCloseJob{})
	return nil
}

func TestPlanValidate_WorkStepNeedsTypo_Suggests(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-step-typo-close",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return typoCloseJobPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-step-typo-close")

	expectPanic(t, func() {
		_, _ = reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-step-typo-close"})
	}, func(msg string) {
		for _, want := range []string{`"fetchh"`, `did you mean "fetch"`, `WorkStep "compile"`, `node "build"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic missing %q\nfull: %s", want, msg)
			}
		}
	})
}

type typoFarJob struct{ sparkwing.Base }

func (typoFarJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs("totallyunrelated")
	return nil, nil
}

type typoFarJobPipe struct{ sparkwing.Base }

func (typoFarJobPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", typoFarJob{})
	return nil
}

func TestPlanValidate_WorkStepNeedsTypo_NoCloseMatchListsAvailable(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-step-typo-far",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return typoFarJobPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-step-typo-far")

	expectPanic(t, func() {
		_, _ = reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-step-typo-far"})
	}, func(msg string) {
		if !strings.Contains(msg, `"totallyunrelated"`) {
			t.Errorf("panic should quote the offending ref, got: %s", msg)
		}
		if strings.Contains(msg, "did you mean") {
			t.Errorf("no close match -> shouldn't suggest, got: %s", msg)
		}
		// Available steps (sorted) should be enumerated.
		if !strings.Contains(msg, "available steps:") || !strings.Contains(msg, "compile") || !strings.Contains(msg, "fetch") {
			t.Errorf("panic should list available steps, got: %s", msg)
		}
	})
}

// Handle-typed Needs(*WorkStep) is the canonical, IDE-checked path
// and must not produce a panic.
type handleNeedsJob struct{ sparkwing.Base }

func (handleNeedsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs(fetch)
	return nil, nil
}

type handleNeedsPipe struct{ sparkwing.Base }

func (handleNeedsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", handleNeedsJob{})
	return nil
}

func TestPlanValidate_HandleNeedsUnaffected(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-handle-needs",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return handleNeedsPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-handle-needs")
	if _, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-handle-needs"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}

// String reference that exactly matches an existing step is fine.
type stringExactJob struct{ sparkwing.Base }

func (stringExactJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs("fetch")
	return nil, nil
}

type stringExactPipe struct{ sparkwing.Base }

func (stringExactPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", stringExactJob{})
	return nil
}

func TestPlanValidate_StringMatchingExistingStep_NoPanic(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-string-exact",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stringExactPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-string-exact")
	if _, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-string-exact"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}

// --- Plan-level (Node.Needs) ---

type planTypoPipe struct{ sparkwing.Base }

func (planTypoPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "compile", func(ctx context.Context) error { return nil }).Needs("fetchh")
	return nil
}

func TestPlanValidate_NodeNeedsTypo_Suggests(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-node-typo",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return planTypoPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-node-typo")

	expectPanic(t, func() {
		_, _ = reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-node-typo"})
	}, func(msg string) {
		for _, want := range []string{`Node "compile"`, `"fetchh"`, `did you mean "fetch"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic missing %q\nfull: %s", want, msg)
			}
		}
	})
}

// --- Spawn handles (string Needs targeting a SpawnNode) ---

type spawnTypoJob struct{ sparkwing.Base }

func (spawnTypoJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "seed", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "after", func(ctx context.Context) error { return nil }).Needs("seedd")
	return nil, nil
}

type spawnTypoPipe struct{ sparkwing.Base }

func (spawnTypoPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", spawnTypoJob{})
	return nil
}

func TestPlanValidate_StringRefToMissingSpawn_Suggests(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-spawn-typo",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return spawnTypoPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-spawn-typo")

	expectPanic(t, func() {
		_, _ = reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-spawn-typo"})
	}, func(msg string) {
		if !strings.Contains(msg, `did you mean "seed"`) {
			t.Errorf("expected suggestion of 'seed' for typo 'seedd', got: %s", msg)
		}
	})
}

// String ref that hits a SpawnNode (exact match) is fine -- spawn
// IDs are part of the same resolution set as steps.
type spawnStringExactJob struct{ sparkwing.Base }

func (spawnStringExactJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "seed", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "after", func(ctx context.Context) error { return nil }).Needs("seed")
	return nil, nil
}

type spawnStringExactPipe struct{ sparkwing.Base }

func (spawnStringExactPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", spawnStringExactJob{})
	return nil
}

func TestPlanValidate_StringRefToExistingSpawn_NoPanic(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plan-validate-spawn-exact",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return spawnStringExactPipe{} })
	reg, _ := sparkwing.Lookup("plan-validate-spawn-exact")
	if _, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-validate-spawn-exact"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}
