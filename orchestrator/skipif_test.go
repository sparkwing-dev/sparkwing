package orchestrator_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// setupOut is the typed output that SkipIf predicates read through
// closure-captured Refs.
type setupOut struct {
	SkipDeploy bool `json:"skip_deploy"`
	SkipTests  bool `json:"skip_tests"`
}

type setupJob struct {
	sparkwing.Base
	sparkwing.Produces[setupOut]
}

// skipDeployWant is module-level so tests can flip it per-case.
var skipDeployWant atomic.Bool

func (j *setupJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (setupJob) run(ctx context.Context) (setupOut, error) {
	return setupOut{SkipDeploy: skipDeployWant.Load()}, nil
}

// --- pipelines ---

// skipOnDeployFlag: deploy is gated on the setup's SkipDeploy output.
// When SkipDeploy=true, deploy should be marked Skipped.
type skipOnDeployFlag struct{ sparkwing.Base }

var deployRan atomic.Bool

func (skipOnDeployFlag) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	setup := sparkwing.Job(plan, "setup", &setupJob{})
	setupRef := sparkwing.RefTo[setupOut](setup)
	sparkwing.Job(plan, "deploy-step", sparkwing.JobFn(func(ctx context.Context) error {
		deployRan.Store(true)
		return nil
	})).
		Needs(setup).
		SkipIf(func(ctx context.Context) bool {
			return setupRef.Get(ctx).SkipDeploy
		})
	return nil
}

// multiplePredicates: two SkipIf accumulate with OR semantics.
type multiPredicatesOR struct{ sparkwing.Base }

var multiRan atomic.Bool

func (multiPredicatesOR) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	setup := sparkwing.Job(plan, "setup", &setupJob{})
	setupRef := sparkwing.RefTo[setupOut](setup)
	sparkwing.Job(plan, "job", sparkwing.JobFn(func(ctx context.Context) error {
		multiRan.Store(true)
		return nil
	})).
		Needs(setup).
		SkipIf(func(ctx context.Context) bool { return false }).
		SkipIf(func(ctx context.Context) bool { return setupRef.Get(ctx).SkipDeploy })
	return nil
}

// slowPredicate exceeds the 1s predicate budget, should default to
// "do not skip".
type slowPredicate struct{ sparkwing.Base }

var slowRan atomic.Bool

func (slowPredicate) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "guarded", sparkwing.JobFn(func(ctx context.Context) error {
		slowRan.Store(true)
		return nil
	})).SkipIf(func(ctx context.Context) bool {
		select {
		case <-time.After(3 * time.Second):
			return true
		case <-ctx.Done():
			return false
		}
	}, sparkwing.SkipBudget(200*time.Millisecond)) // tight budget for the test
	return nil
}

// panickyPredicate panics; orchestrator should catch and default to
// "do not skip".
type panickyPredicate struct{ sparkwing.Base }

var panickyRan atomic.Bool

func (panickyPredicate) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "guarded", sparkwing.JobFn(func(ctx context.Context) error {
		panickyRan.Store(true)
		return nil
	})).SkipIf(func(ctx context.Context) bool {
		panic("oops")
	})
	return nil
}

// downstream should still consider a Skipped upstream as OK-to-proceed.
type skippedUpstream struct{ sparkwing.Base }

var downstreamRan atomic.Bool

func (skippedUpstream) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	skipMe := sparkwing.Job(plan, "skip-me", sparkwing.JobFn(func(ctx context.Context) error {
		return nil
	})).SkipIf(func(ctx context.Context) bool { return true })
	sparkwing.Job(plan, "downstream", sparkwing.JobFn(func(ctx context.Context) error {
		downstreamRan.Store(true)
		return nil
	})).Needs(skipMe)
	return nil
}

func init() {
	register("skipif-flag", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &skipOnDeployFlag{} })
	register("skipif-multi", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &multiPredicatesOR{} })
	register("skipif-slow", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &slowPredicate{} })
	register("skipif-panic", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &panickyPredicate{} })
	register("skipif-downstream", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &skippedUpstream{} })
}

// --- tests ---

func TestSkipIf_SkipsWhenPredicateTrue(t *testing.T) {
	skipDeployWant.Store(true)
	deployRan.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-flag"})

	if res.Status != "success" {
		t.Fatalf("run status = %q, want success (skipped node shouldn't fail the run)", res.Status)
	}
	if deployRan.Load() {
		t.Fatal("deploy should not have executed")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	deploy := byID["deploy-step"]
	if deploy.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("deploy outcome = %q, want skipped", deploy.Outcome)
	}
	if !strings.Contains(deploy.Error, "predicate") {
		t.Fatalf("skip reason should mention predicate, got %q", deploy.Error)
	}
}

func TestSkipIf_RunsWhenPredicateFalse(t *testing.T) {
	skipDeployWant.Store(false)
	deployRan.Store(false)
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-flag"})

	if !deployRan.Load() {
		t.Fatal("deploy should have executed when predicate returns false")
	}
}

func TestSkipIf_MultiplePredicatesOR(t *testing.T) {
	skipDeployWant.Store(true) // second predicate should return true
	multiRan.Store(false)
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-multi"})

	if multiRan.Load() {
		t.Fatal("job should be skipped when any predicate returns true")
	}
}

func TestSkipIf_MultiplePredicatesAllFalse(t *testing.T) {
	skipDeployWant.Store(false)
	multiRan.Store(false)
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-multi"})

	if !multiRan.Load() {
		t.Fatal("job should run when all predicates return false")
	}
}

func TestSkipIf_SlowPredicateDefaultsToRun(t *testing.T) {
	slowRan.Store(false)
	p := newPaths(t)
	start := time.Now()
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-slow"})
	elapsed := time.Since(start)

	// Predicate budget on the pipeline is 200ms (explicitly set via
	// SkipBudget); run must finish quickly and the job must run.
	if elapsed > 1*time.Second {
		t.Fatalf("slow predicate should time out near 200ms, took %s", elapsed)
	}
	if !slowRan.Load() {
		t.Fatal("job should run when predicate times out")
	}
}

func TestSkipIf_PanickyPredicateDefaultsToRun(t *testing.T) {
	panickyRan.Store(false)
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-panic"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !panickyRan.Load() {
		t.Fatal("job should run when predicate panics")
	}
}

// generousTimeout: the default budget is 30s; configure a tiny 50ms
// budget explicitly and verify it's honored.
type generousTimeout struct{ sparkwing.Base }

var generousRan atomic.Bool

func (generousTimeout) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "gated", sparkwing.JobFn(func(ctx context.Context) error {
		generousRan.Store(true)
		return nil
	})).SkipIf(func(ctx context.Context) bool {
		time.Sleep(500 * time.Millisecond)
		return true
	}, sparkwing.SkipBudget(50*time.Millisecond))
	return nil
}

func init() {
	register("skipif-generous", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &generousTimeout{} })
}

func TestSkipIf_PerNodeTimeoutOverride(t *testing.T) {
	generousRan.Store(false)
	p := newPaths(t)
	start := time.Now()
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-generous"})
	elapsed := time.Since(start)

	// 50ms budget, predicate sleeps 500ms -> timeout fires, job runs.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("override budget should fire near 50ms, took %s", elapsed)
	}
	if !generousRan.Load() {
		t.Fatal("job should run after predicate times out under override budget")
	}
}

func TestSkipIf_DownstreamProceedsAfterSkip(t *testing.T) {
	downstreamRan.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "skipif-downstream"})

	if res.Status != "success" {
		t.Fatalf("run status = %q", res.Status)
	}
	if !downstreamRan.Load() {
		t.Fatal("downstream should have run after upstream was skipped")
	}
}
