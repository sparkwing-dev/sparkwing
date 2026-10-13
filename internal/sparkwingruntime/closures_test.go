package sparkwingruntime_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type closureSource struct {
	sparkwing.Base
	sparkwing.Produces[[]string]
}

func (closureSource) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "list", func(context.Context) ([]string, error) { return nil, nil }), nil
}

type closureSteps struct{}

func (closureSteps) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "b", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return true })
	return nil, nil
}

func noop(context.Context) error { return nil }

func skip(context.Context) bool { return false }

func allClosureIDs(c *nodeproto.PlanClosures) []string {
	if c == nil {
		return nil
	}
	ids := slices.Concat(c.SkipIf, c.BeforeRun, c.AfterRun, c.Generator)
	for _, id := range []string{c.Verify, c.CacheKey} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestClosureIDsAreUniqueWhenIDsHoldSlashes(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "a", closureSteps{}).
		BeforeRun(noop).
		BeforeRun(noop).
		AfterRun(func(context.Context, error) {}).
		Verify(noop).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return sparkwing.Key("a"), nil }).
		OnFailure("a/rollback", noop).
		SkipIf(skip)
	sparkwing.Job(plan, "a/b", noop).SkipIf(skip)
	sparkwing.Job(plan, "a/step/b", noop).SkipIf(skip)
	sparkwing.Job(plan, "a%2Fb", noop).SkipIf(skip)
	src := sparkwing.Job(plan, "src", &closureSource{})
	sparkwing.JobFanOutDynamic(plan, "fan", src, func(s string) (string, any) { return s, noop })
	plan.Job("a/rollback").SkipIf(skip)

	nodeA, stepsA := sparkwingruntime.NodeClosureIDs(plan, plan.Job("a"))
	want := &nodeproto.PlanClosures{
		SkipIf:    []string{"a/skip_if/0"},
		BeforeRun: []string{"a/before_run/0", "a/before_run/1"},
		AfterRun:  []string{"a/after_run/0"},
		Verify:    "a/verify/0",
		CacheKey:  "a/cache_key/0",
	}
	if !reflect.DeepEqual(nodeA, want) {
		t.Errorf("node a closures = %+v, want %+v", nodeA, want)
	}
	if got := stepsA["b"]; got == nil || !slices.Equal(got.SkipIf, []string{"a/step/b/skip_if/0"}) {
		t.Errorf("step a/b closures = %+v, want skip_if a/step/b/skip_if/0", got)
	}

	owners := map[string]string{}
	for _, id := range []string{"a", "a/b", "a/step/b", "a%2Fb", "src", "a/rollback"} {
		n := plan.Job(id)
		node, steps := sparkwingruntime.NodeClosureIDs(plan, n)
		ids := allClosureIDs(node)
		for _, sc := range steps {
			ids = append(ids, allClosureIDs(sc)...)
		}
		if len(ids) == 0 {
			t.Errorf("node %q has no closure ids", id)
		}
		for _, cid := range ids {
			if prev, dup := owners[cid]; dup {
				t.Errorf("closure id %q belongs to both %q and %q", cid, prev, id)
			}
			owners[cid] = id
			c, ok := sparkwingruntime.LookupClosure(plan, cid)
			if !ok || c.Node != n || c.Fn == nil {
				t.Errorf("LookupClosure(%q) = %+v, %v; want a closure of node %q", cid, c, ok, id)
			}
		}
	}
	if got := owners["src/generator/0"]; got != "src" {
		t.Errorf("generator id belongs to %q, want src", got)
	}
	if c, _ := sparkwingruntime.LookupClosure(plan, "a/step/b/skip_if/0"); c.Step == nil || c.Step.ID() != "b" || c.Kind != sparkwingruntime.ClosureSkipIf {
		t.Errorf("step closure lookup = %+v, want skip_if of step b", c)
	}
	if _, ok := sparkwingruntime.LookupClosure(plan, "a/verify/1"); ok {
		t.Error("LookupClosure resolved an id no closure was assigned")
	}
}
