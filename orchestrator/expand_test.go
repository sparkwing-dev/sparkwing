package orchestrator_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// These tests cover the orchestrator's runtime fan-out machinery via
// the JobFanOutDynamic surface (formerly NodeForEach, originally the
// replacement for ExpandFrom).

type discoverJob struct {
	sparkwing.Base
	sparkwing.Produces[[]string]
}

// discoverItems is how each test case plants the list the generator
// will see. Set to some non-nil value before Run().
var discoverItems atomic.Pointer[[]string]

func (j *discoverJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (discoverJob) run(ctx context.Context) ([]string, error) {
	p := discoverItems.Load()
	if p == nil {
		return nil, nil
	}
	return *p, nil
}

// expandOK: discover emits N images, JobFanOutDynamic fans out N
// build-<img> nodes, a downstream fanin depends on the whole group.
type expandOK struct{ sparkwing.Base }

var (
	builtMu     sync.Mutex
	builtImages []string
	faninRan    atomic.Bool
)

func (expandOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	discover := sparkwing.Job(plan, "discover", &discoverJob{})
	group := sparkwing.JobFanOutDynamic(plan, "builds", discover, func(img string) (string, any) {
		return "build-" + img, &recordingBuild{image: img}
	})
	sparkwing.Job(plan, "fanin", func(ctx context.Context) error {
		faninRan.Store(true)
		return nil
	}).Needs(group)
	return nil
}

// recordingBuild appends its image name to builtImages when run.
type recordingBuild struct {
	sparkwing.Base
	image string
}

func (r *recordingBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", r.run)
	return nil, nil
}

func (r *recordingBuild) run(ctx context.Context) error {
	builtMu.Lock()
	builtImages = append(builtImages, r.image)
	builtMu.Unlock()
	return nil
}

// expandEmpty: source emits zero items. Generator produces zero
// children. Downstream fanin should still dispatch (an empty group is
// a no-op dependency).
type expandEmpty struct{ sparkwing.Base }

var emptyFaninRan atomic.Bool

func (expandEmpty) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	discover := sparkwing.Job(plan, "discover", &discoverJob{})
	group := sparkwing.JobFanOutDynamic(plan, "builds", discover, func(img string) (string, any) {
		return "should-not-create-" + img, func(ctx context.Context) error { return nil }
	})
	sparkwing.Job(plan, "fanin", func(ctx context.Context) error {
		emptyFaninRan.Store(true)
		return nil
	}).Needs(group)
	return nil
}

// expandSourceFails: discover fails; generator should not run; fanin
// should cancel.
type expandSourceFails struct{ sparkwing.Base }

var failedFaninRan atomic.Bool
var failedGenRan atomic.Bool

type failingDiscover struct {
	sparkwing.Base
	sparkwing.Produces[[]string]
}

func (j *failingDiscover) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (failingDiscover) run(ctx context.Context) ([]string, error) {
	return nil, errors.New("discover broken")
}

func (expandSourceFails) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	discover := sparkwing.Job(plan, "discover", &failingDiscover{})
	group := sparkwing.JobFanOutDynamic(plan, "builds", discover, func(img string) (string, any) {
		failedGenRan.Store(true)
		return "should-not-create-" + img, func(ctx context.Context) error { return nil }
	})
	sparkwing.Job(plan, "fanin", func(ctx context.Context) error {
		failedFaninRan.Store(true)
		return nil
	}).Needs(group)
	return nil
}

// expandGenPanics: generator panics; orchestrator recovers, marks
// group with error, downstream cancels cleanly.
type expandGenPanics struct{ sparkwing.Base }

var panicFaninRan atomic.Bool

func (expandGenPanics) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	discover := sparkwing.Job(plan, "discover", &discoverJob{})
	group := sparkwing.JobFanOutDynamic(plan, "builds", discover, func(img string) (string, any) {
		panic("boom")
	})
	sparkwing.Job(plan, "fanin", func(ctx context.Context) error {
		panicFaninRan.Store(true)
		return nil
	}).Needs(group)
	return nil
}

func init() {
	register("expand-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &expandOK{} })
	register("expand-empty", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &expandEmpty{} })
	register("expand-source-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &expandSourceFails{} })
	register("expand-gen-panics", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &expandGenPanics{} })
}

// --- Tests ---

func TestJobFanOutDynamic_FansOutAndJoins(t *testing.T) {
	items := []string{"alpha", "beta", "gamma"}
	discoverItems.Store(&items)
	builtMu.Lock()
	builtImages = nil
	builtMu.Unlock()
	faninRan.Store(false)

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "expand-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}
	if !faninRan.Load() {
		t.Fatal("fanin should have run")
	}

	builtMu.Lock()
	built := append([]string{}, builtImages...)
	builtMu.Unlock()
	sort.Strings(built)
	wantSorted := []string{"alpha", "beta", "gamma"}
	if !stringSliceEq(built, wantSorted) {
		t.Fatalf("built = %v, want %v", built, wantSorted)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	nodeIDs := map[string]string{}
	for _, n := range nodes {
		nodeIDs[n.NodeID] = n.Outcome
	}
	for _, img := range items {
		if nodeIDs["build-"+img] != string(sparkwing.Success) {
			t.Fatalf("node build-%s outcome = %q, want success", img, nodeIDs["build-"+img])
		}
	}
}

func TestJobFanOutDynamic_EmptyExpansion(t *testing.T) {
	empty := []string{}
	discoverItems.Store(&empty)
	emptyFaninRan.Store(false)

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "expand-empty"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}
	if !emptyFaninRan.Load() {
		t.Fatal("fanin should run even when expansion produces no children")
	}
}

func TestJobFanOutDynamic_SourceFailsCancelsFanin(t *testing.T) {
	failedFaninRan.Store(false)
	failedGenRan.Store(false)

	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "expand-source-fails"})

	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if failedGenRan.Load() {
		t.Fatal("generator should not run when source fails")
	}
	if failedFaninRan.Load() {
		t.Fatal("fanin should not run when its dynamic upstream didn't resolve")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["fanin"].Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("fanin outcome = %q, want cancelled", byID["fanin"].Outcome)
	}
}

func TestJobFanOutDynamic_GeneratorPanicCancelsFanin(t *testing.T) {
	items := []string{"one"}
	discoverItems.Store(&items)
	panicFaninRan.Store(false)

	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "expand-gen-panics"})

	if panicFaninRan.Load() {
		t.Fatal("fanin should not run after generator panic")
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
