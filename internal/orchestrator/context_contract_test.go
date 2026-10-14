package orchestrator_test

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

var contextProbeOrder = []string{
	"Pipeline.Plan",
	"JobNode.SkipIf",
	"CacheKeyFn",
	"Job body step",
	"Verify",
	"BeforeRun",
	"AfterRun",
}

type contextSighting struct {
	node, step  string
	sideEffects bool
}

var (
	seenMu sync.Mutex
	seen   map[string]contextSighting
)

func probeContext(ctx context.Context, site string) {
	seenMu.Lock()
	defer seenMu.Unlock()
	if seen == nil {
		seen = map[string]contextSighting{}
	}
	seen[site] = contextSighting{
		node:        sparkwing.NodeFromContext(ctx),
		step:        sparkwing.StepFromContext(ctx),
		sideEffects: guardAllows(ctx),
	}
}

func guardAllows(ctx context.Context) (ok bool) {
	defer func() { ok = recover() == nil }()
	planguard.Guard(ctx, "probe")
	return true
}

type contextProbePipe struct{ sparkwing.Base }

func (contextProbePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	probeContext(ctx, "Pipeline.Plan")
	sparkwing.Job(plan, "probe", func(ctx context.Context) error {
		probeContext(ctx, "Job body step")
		return nil
	}).
		SkipIf(func(ctx context.Context) bool {
			probeContext(ctx, "JobNode.SkipIf")
			return false
		}).
		Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
			probeContext(ctx, "CacheKeyFn")
			return sparkwing.NoCache, nil
		}).
		Verify(func(ctx context.Context) error {
			probeContext(ctx, "Verify")
			return nil
		}).
		BeforeRun(func(ctx context.Context) error {
			probeContext(ctx, "BeforeRun")
			return nil
		}).
		AfterRun(func(ctx context.Context, _ error) {
			probeContext(ctx, "AfterRun")
		})
	return nil
}

func init() {
	register("orch-context-probe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &contextProbePipe{} })
}

func runContextProbe(t *testing.T) map[string]contextSighting {
	t.Helper()
	seenMu.Lock()
	seen = nil
	seenMu.Unlock()

	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-context-probe"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	seenMu.Lock()
	defer seenMu.Unlock()
	var missing []string
	for _, site := range contextProbeOrder {
		if _, ok := seen[site]; !ok {
			missing = append(missing, site)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these probes never ran, so the run proves nothing about them: %v", missing)
	}
	out := make(map[string]contextSighting, len(seen))
	for k, v := range seen {
		out[k] = v
	}
	return out
}

func TestRun_PlanIsTheOnlyCallbackThatRefusesSideEffects(t *testing.T) {
	for site, s := range runContextProbe(t) {
		if site == "Pipeline.Plan" {
			if s.sideEffects {
				t.Error("Pipeline.Plan allowed a side-effect helper; the purity guard is not sealing")
			}
			continue
		}
		if !s.sideEffects {
			t.Errorf("%s refused a side-effect helper; the Plan seal outlived Plan", site)
		}
	}
}
