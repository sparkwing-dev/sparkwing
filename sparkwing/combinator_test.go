package sparkwing_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

func TestJobFanOut_StaticMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	items := []string{"a", "b", "c"}
	g := sparkwing.JobFanOut(plan, "builds", items, func(s string) (string, any) {
		return "build-" + s, func(ctx context.Context) error { return nil }
	})
	if g == nil {
		t.Fatal("JobFanOut should return a Group")
	}
	if g.Name() != "builds" {
		t.Fatalf("group name = %q, want %q", g.Name(), "builds")
	}
	if g.Dynamic() {
		t.Fatal("JobFanOut group should be static, not dynamic")
	}
	got := []string{}
	for _, m := range g.Members() {
		got = append(got, m.ID())
	}
	want := []string{"build-a", "build-b", "build-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for _, id := range want {
		if plan.Node(id) == nil {
			t.Fatalf("plan should contain node %q", id)
		}
	}
}

func TestJobFanOut_EmptyItemsReturnsEmptyGroup(t *testing.T) {
	plan := sparkwing.NewPlan()
	calls := 0
	g := sparkwing.JobFanOut(plan, "noop", []int{}, func(int) (string, any) {
		calls++
		return "x", func(ctx context.Context) error { return nil }
	})
	if g == nil {
		t.Fatal("JobFanOut should return a Group even when items is empty")
	}
	if len(g.Members()) != 0 {
		t.Fatalf("expected 0 members, got %d", len(g.Members()))
	}
	if calls != 0 {
		t.Fatalf("fn should not run on empty items; ran %d times", calls)
	}
	consumer := sparkwing.Job(plan, "after", func(ctx context.Context) error { return nil })
	consumer.Needs(g)
	if deps := consumer.DepIDs(); len(deps) != 0 {
		t.Fatalf("Needs(empty group) should add no deps; got %v", deps)
	}
}

func TestGroup_NeedsAppliesToEveryMember(t *testing.T) {
	plan := sparkwing.NewPlan()
	upstream := sparkwing.Job(plan, "upstream", func(ctx context.Context) error { return nil })
	g := sparkwing.JobFanOut(plan, "builds", []string{"a", "b"}, func(s string) (string, any) {
		return "build-" + s, func(ctx context.Context) error { return nil }
	}).Needs(upstream)
	for _, m := range g.Members() {
		if !slices.Contains(m.DepIDs(), "upstream") {
			t.Fatalf("member %q should depend on upstream; deps=%v", m.ID(), m.DepIDs())
		}
	}
}

func TestGroup_RetryAndTimeoutApplyToEveryMember(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.JobFanOut(plan, "builds", []string{"a", "b"}, func(s string) (string, any) {
		return "build-" + s, func(ctx context.Context) error { return nil }
	}).Retry(3).Timeout(10 * time.Second)
	for _, m := range g.Members() {
		if got := m.RetryConfig().Attempts; got != 3 {
			t.Fatalf("member %q retry attempts = %d, want 3", m.ID(), got)
		}
		if got := m.TimeoutDuration(); got != 10*time.Second {
			t.Fatalf("member %q timeout = %v, want 10s", m.ID(), got)
		}
	}
}

// TestGroupModifiersMirrorNode is the drift-protection guard required
// by SDK-029. Every chainable *Node modifier (one returning *Node)
// should have a *NodeGroup counterpart returning *NodeGroup, applied uniformly
// to every member. OnFailure is intentionally exempt: recovery handlers
// are per-node by intent.
func TestGroupModifiersMirrorNode(t *testing.T) {
	exempt := map[string]bool{
		// Recovery handlers are per-node by intent (SDK-029).
		"OnFailure": true,
		// Dynamic() hand-marks a Node for renderer purposes; Group
		// dynamism is already a structural property (JobFanOutDynamic
		// produces a dynamic group; static helpers don't).
		"Dynamic": true,
		// OnFailureNode is a getter that accidentally matches the
		// chainable shape (returns *Node).
		"OnFailureNode": true,
	}
	nodeChainable := chainableMethods(reflect.TypeOf(&sparkwing.Node{}))
	groupChainable := chainableMethods(reflect.TypeOf(&sparkwing.NodeGroup{}))
	groupSet := map[string]bool{}
	for _, m := range groupChainable {
		groupSet[m] = true
	}
	for _, name := range nodeChainable {
		if exempt[name] {
			continue
		}
		if !groupSet[name] {
			t.Errorf("chainable Node.%s has no Group counterpart (add it to combinator.go or update the exempt list)", name)
		}
	}
}

// chainableMethods returns the names of methods on t that take only
// the receiver-or-trailing args and return the same type t (i.e. the
// builder-chainable surface). Methods are considered chainable when
// their single return value is t itself.
func chainableMethods(t reflect.Type) []string {
	var names []string
	for i := range t.NumMethod() {
		m := t.Method(i)
		if m.Type.NumOut() != 1 {
			continue
		}
		if m.Type.Out(0) != t {
			continue
		}
		names = append(names, m.Name)
	}
	return names
}
