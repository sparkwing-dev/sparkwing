package sparkwing_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// requiresJob declares only Requires.
type requiresJob struct{ sparkwing.Base }

func (requiresJob) Requires() []string { return []string{"cloud-windows"} }
func (requiresJob) Work(_ *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return nil, nil
}

// allThreeJob declares all three provider interfaces.
type allThreeJob struct{ sparkwing.Base }

func (allThreeJob) Requires() []string   { return []string{"os=linux"} }
func (allThreeJob) Prefers() []string    { return []string{"cloud-linux"} }
func (allThreeJob) WhenRunner() []string { return []string{"local,cloud-linux"} }
func (allThreeJob) Work(_ *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return nil, nil
}

// shardJob picks Requires per its data — the canonical heterogeneous
// fan-out use case.
type shardJob struct {
	sparkwing.Base
	NeedsUSB bool
}

func (s shardJob) Requires() []string {
	if s.NeedsUSB {
		return []string{"local"}
	}
	return []string{"cloud-linux"}
}
func (s shardJob) Work(_ *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return nil, nil
}

func TestWorkableLabels_RequiresProvider(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &requiresJob{})
	got := n.RequiresLabels()
	if !reflect.DeepEqual(got, []string{"cloud-windows"}) {
		t.Fatalf("RequiresLabels = %v, want [cloud-windows]", got)
	}
	if got := n.PrefersLabels(); got != nil {
		t.Errorf("PrefersLabels = %v, want nil (no provider)", got)
	}
	if got := n.WhenRunnerLabels(); got != nil {
		t.Errorf("WhenRunnerLabels = %v, want nil (no provider)", got)
	}
}

func TestWorkableLabels_AllThreeProviders(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &allThreeJob{})
	if got := n.RequiresLabels(); !reflect.DeepEqual(got, []string{"os=linux"}) {
		t.Errorf("RequiresLabels = %v", got)
	}
	if got := n.PrefersLabels(); !reflect.DeepEqual(got, []string{"cloud-linux"}) {
		t.Errorf("PrefersLabels = %v", got)
	}
	if got := n.WhenRunnerLabels(); !reflect.DeepEqual(got, []string{"local,cloud-linux"}) {
		t.Errorf("WhenRunnerLabels = %v", got)
	}
}

func TestWorkableLabels_ChainableOverwritesProvider(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &requiresJob{}).Requires("override")
	if got := n.RequiresLabels(); !reflect.DeepEqual(got, []string{"override"}) {
		t.Fatalf("RequiresLabels = %v, want [override] (chainable replaces provider)", got)
	}
}

func TestWorkableLabels_ChainableNoArgsClearsProviderValue(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &requiresJob{}).Requires()
	if got := n.RequiresLabels(); got != nil {
		t.Fatalf("RequiresLabels = %v, want nil after empty-call clears", got)
	}
}

func TestWorkableLabels_JobFanOutHeterogeneous(t *testing.T) {
	plan := sparkwing.NewPlan()
	specs := []shardJob{
		{NeedsUSB: true},
		{NeedsUSB: false},
		{NeedsUSB: true},
	}
	indices := []int{0, 1, 2}
	sparkwing.JobFanOut(plan, "shards", indices, func(i int) (string, any) {
		return fmt.Sprintf("shard-%d", i), specs[i]
	})
	want := map[string][]string{
		"shard-0": {"local"},
		"shard-1": {"cloud-linux"},
		"shard-2": {"local"},
	}
	for id, w := range want {
		n := plan.Job(id)
		if n == nil {
			t.Fatalf("missing %s", id)
		}
		if got := n.RequiresLabels(); !reflect.DeepEqual(got, w) {
			t.Errorf("%s RequiresLabels = %v, want %v", id, got, w)
		}
	}
}

// dynShardSource produces []int the JobFanOutDynamic gen reads. The
// per-item Workable's Requires() then depends on whether i is odd.
type dynShardSource struct {
	sparkwing.Base
	sparkwing.Produces[[]int]
}

func (dynShardSource) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "discover", func(ctx context.Context) ([]int, error) {
		return []int{0, 1, 2, 3}, nil
	}), nil
}

func TestWorkableLabels_JobFanOutDynamicHeterogeneous(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "discover", &dynShardSource{})
	sparkwing.JobFanOutDynamic(plan, "shards", source, func(i int) (string, any) {
		return fmt.Sprintf("shard-%d", i), shardJob{NeedsUSB: i%2 == 0}
	})
	// Dynamic fan-out generators run at dispatch time. Trigger the
	// generator manually by retrieving the registered expansion and
	// invoking it with a resolver carrying the source's output.
	exps := plan.Expansions()
	if len(exps) != 1 {
		t.Fatalf("expected one expansion, got %d", len(exps))
	}
	resolver := func(nodeID string) (any, bool) {
		if nodeID == "discover" {
			return []int{0, 1, 2, 3}, true
		}
		return nil, false
	}
	ctx := sparkwing.WithResolver(context.Background(), resolver)
	children := exps[0].Gen(ctx)
	if len(children) != 4 {
		t.Fatalf("expected 4 children, got %d", len(children))
	}
	want := map[string][]string{
		"shard-0": {"cloud-linux"}, // i%2==0 -> NeedsUSB=true ... wait inverted
	}
	_ = want
	// shardJob.NeedsUSB toggles the labels: NeedsUSB=true -> ["local"];
	// NeedsUSB=false -> ["cloud-linux"]. With NeedsUSB = (i%2 == 0):
	//   i=0 -> NeedsUSB=true  -> local
	//   i=1 -> NeedsUSB=false -> cloud-linux
	//   i=2 -> NeedsUSB=true  -> local
	//   i=3 -> NeedsUSB=false -> cloud-linux
	gotLabels := map[string][]string{}
	for _, c := range children {
		gotLabels[c.ID()] = c.RequiresLabels()
	}
	expect := map[string][]string{
		"shard-0": {"local"},
		"shard-1": {"cloud-linux"},
		"shard-2": {"local"},
		"shard-3": {"cloud-linux"},
	}
	for id, w := range expect {
		if !reflect.DeepEqual(gotLabels[id], w) {
			t.Errorf("%s RequiresLabels = %v, want %v", id, gotLabels[id], w)
		}
	}
}

func TestWorkableLabels_BareFuncClosureLeavesLabelsEmpty(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", func(ctx context.Context) error { return nil })
	if got := n.RequiresLabels(); got != nil {
		t.Errorf("RequiresLabels = %v, want nil for bare closure", got)
	}
	if got := n.PrefersLabels(); got != nil {
		t.Errorf("PrefersLabels = %v, want nil for bare closure", got)
	}
	if got := n.WhenRunnerLabels(); got != nil {
		t.Errorf("WhenRunnerLabels = %v, want nil for bare closure", got)
	}
}
