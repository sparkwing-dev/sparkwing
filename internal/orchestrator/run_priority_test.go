package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var priorityPipeRegister sync.Once

type priorityDeclaringPipe struct{ sparkwing.Base }

func (priorityDeclaringPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Priority(2)
	sparkwing.Job(plan, "quick", func(context.Context) error { return nil })
	return nil
}

func registerPriorityPipeline() {
	priorityPipeRegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-declares-priority",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return priorityDeclaringPipe{} })
	})
}

func TestResolveRunPriority_LiteralIntegerNeverAsksTheQueue(t *testing.T) {
	// safety: Home names a directory with no daemon, so a query would fail;
	// a literal value must not make one.
	admission := testWingdAdmission(t.TempDir(), nil)
	for _, tc := range []struct {
		raw  string
		want int
	}{{"42", 42}, {"0", 0}, {"-9", -9}, {" 7 ", 7}} {
		got, set, err := resolveRunPriority(context.Background(), tc.raw, admission)
		if err != nil {
			t.Fatalf("resolveRunPriority(%q) = %v", tc.raw, err)
		}
		if !set || got.value != tc.want || got.source != "flag" {
			t.Errorf("resolveRunPriority(%q) = %+v set=%v, want %d/flag", tc.raw, got, set, tc.want)
		}
	}
}

func TestResolveRunPriority_EmptyRequestLeavesThePlanAlone(t *testing.T) {
	got, set, err := resolveRunPriority(context.Background(), "  ", testWingdAdmission(t.TempDir(), nil))
	if err != nil || set || got.source != "" {
		t.Fatalf("resolveRunPriority(blank) = %+v set=%v err=%v, want an untouched plan", got, set, err)
	}
}

// safety: an absent daemon is an empty queue rather than a failure, so a
// priority request never blocks a run that reaches no daemon at all.
func TestResolveRunPriority_NoDaemonIsAnEmptyQueue(t *testing.T) {
	admission := testWingdAdmission(t.TempDir(), nil)
	for _, tc := range []struct {
		mode string
		want int
	}{{"front", 1}, {"back", -1}} {
		got, set, err := resolveRunPriority(context.Background(), tc.mode, admission)
		if err != nil {
			t.Fatalf("resolveRunPriority(%q) = %v", tc.mode, err)
		}
		if !set || got.value != tc.want || got.source != tc.mode {
			t.Errorf("resolveRunPriority(%q) = %+v, want %d/%s", tc.mode, got, tc.want, tc.mode)
		}
	}
}

func TestResolveRunPriority_RejectsAnUnusableValue(t *testing.T) {
	_, _, err := resolveRunPriority(context.Background(), "sideways", testWingdAdmission(t.TempDir(), nil))
	if err == nil {
		t.Fatal("an unusable priority was accepted")
	}
	for _, want := range []string{PriorityEnv, "front", "back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestResolveRunPriority_FrontMeasuresTheLiveQueue(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, _, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	holder := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-prio-holder",
			Admission: testWingdAdmission(home, nil),
		})
		holder <- res
	}()
	gate.awaitStarted(t, "wingd-prio-holder")

	waiter := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-prio-waiter",
			Priority:  "5",
			Admission: testWingdAdmission(home, nil),
		})
		waiter <- res
	}()
	awaitWaiter(t, home, "wingd-prio-waiter")

	admission := testWingdAdmission(home, nil)
	front, _, err := resolveRunPriority(context.Background(), "front", admission)
	if err != nil {
		t.Fatalf("front: %v", err)
	}
	if front.value != 6 {
		t.Errorf("front = %d against a queue holding priority 5, want 6", front.value)
	}
	back, _, err := resolveRunPriority(context.Background(), "back", admission)
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if back.value != 4 {
		t.Errorf("back = %d against a queue holding priority 5, want 4", back.value)
	}

	close(gate.release)
	for _, ch := range []chan *Result{holder, waiter} {
		select {
		case res := <-ch:
			if res == nil || res.Status != "success" {
				t.Fatalf("run result = %+v, want success", res)
			}
		case <-time.After(wingdTestWait):
			t.Fatal("run did not finish after release")
		}
	}
}

func TestRunPriority_FlagOverridesThePlanInSnapshotAndRecord(t *testing.T) {
	registerPriorityPipeline()
	home := wingdTestHome(t)
	startWingd(t, home, 4)
	backends, st, _ := openWingdBackends(t, home)

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-declares-priority",
		RunID:     "wingd-prio-override",
		Priority:  "50",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil || res == nil || res.Status != "success" {
		t.Fatalf("run = %+v err=%v, want success", res, err)
	}

	run, err := st.GetRun(context.Background(), "wingd-prio-override")
	if err != nil {
		t.Fatal(err)
	}
	if got := planPriorityFromSnapshot(run.PlanSnapshot); got != 50 {
		t.Errorf("snapshot priority = %d, want the operator's 50 over the plan's 2", got)
	}
	flags, _ := run.Invocation["flags"].(map[string]any)
	if flags == nil {
		t.Fatalf("run invocation records no flags: %+v", run.Invocation)
	}
	if got, ok := flags["priority"].(float64); !ok || int(got) != 50 {
		t.Errorf("recorded priority = %v, want 50", flags["priority"])
	}
	if got, _ := flags["priority_source"].(string); got != "flag" {
		t.Errorf("recorded priority_source = %q, want flag", got)
	}
}

func TestBuildRunFlags_OmitsPriorityWhenUnasked(t *testing.T) {
	flags := buildRunFlags(Options{Pipeline: "demo"})
	if _, ok := flags["priority"]; ok {
		t.Errorf("a run with no --sw-priority recorded one: %+v", flags)
	}
	if _, ok := flags["priority_source"]; ok {
		t.Errorf("a run with no --sw-priority recorded a source: %+v", flags)
	}
}

func TestBuildReproducer_RendersPriorityAsItsFlag(t *testing.T) {
	opts := Options{
		Pipeline:    "demo",
		prioritySet: true,
		priority:    priorityRequest{value: 6, source: "front"},
	}
	got := buildReproducer(opts, "")
	if !strings.Contains(got, "--sw-priority=6") {
		t.Errorf("reproducer = %q, want --sw-priority=6", got)
	}
	if strings.Contains(got, "priority-source") {
		t.Errorf("reproducer = %q, want no priority-source flag", got)
	}
}
