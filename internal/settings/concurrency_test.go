package settings

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestResolveConcurrencyDefaultsAndExplicitPolicy(t *testing.T) {
	declarations := []nodeproto.ConcurrencySpec{{Group: "default"}, {Group: "explicit", Scope: new("box"), Capacity: new(4), Cost: new(2), OnLimit: new("cancel_others"), QueueTimeoutMS: new(int64(200))}}
	got, err := ResolveConcurrency(declarations)
	want := []runspec.Concurrency{{Group: "default", Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue"}, {Group: "explicit", Scope: "box", Capacity: 4, Cost: 2, OnLimit: "cancel_others", QueueTimeoutMS: new(int64(200))}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved=%+v err=%v", got, err)
	}
	if declarations[0].Scope != nil || declarations[0].Capacity != nil || declarations[0].OnLimit != nil {
		t.Fatal("declarations mutated")
	}
}

func TestResolveConcurrencyRejectsInvalidDeclarations(t *testing.T) {
	for _, declaration := range []nodeproto.ConcurrencySpec{{}, {Group: "db", Capacity: new(0)}, {Group: "db", Cost: new(0)}, {Group: "db", Cost: new(2)}, {Group: "db", Scope: new("")}, {Group: "db", OnLimit: new("")}, {Group: "db", QueueTimeoutMS: new(int64(-1))}} {
		if _, err := ResolveConcurrency([]nodeproto.ConcurrencySpec{declaration}); err == nil {
			t.Errorf("accepted=%+v", declaration)
		}
	}
	if _, err := ResolveConcurrency([]nodeproto.ConcurrencySpec{{Group: "db"}, {Group: "db"}}); err == nil {
		t.Fatal("duplicate membership accepted")
	}
}

func TestResolvePlanAndExpansionPreserveConcurrency(t *testing.T) {
	plan := nodeproto.PlanDoc{Concurrency: []nodeproto.ConcurrencySpec{{Group: "deploy", Capacity: new(2)}}, Jobs: []nodeproto.JobSpec{{ID: "a", Concurrency: []nodeproto.ConcurrencySpec{{Group: "db", Capacity: new(3), Cost: new(2)}}}}}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := ResolvePlan(PlanInputs{Declarations: declarations, Plan: plan})
	if err != nil || len(resolved.Concurrency) != 1 || len(resolved.Jobs["a"].Concurrency) != 1 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	spec := runspec.Spec{Declarations: declarations, Resolved: runspec.Resolved{Settings: resolved}}
	expanded := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "b", Concurrency: []nodeproto.ConcurrencySpec{{Group: "db", Capacity: new(3), Cost: new(1)}}}}}
	jobs, err := ResolveExpansion(ExpansionInputs{Spec: spec, Plan: expanded})
	if err != nil || len(jobs["b"].Concurrency) != 1 || jobs["b"].Concurrency[0].Cost != 1 {
		t.Fatalf("expanded=%+v err=%v", jobs, err)
	}
	expanded.Jobs[0].Concurrency[0].Capacity = new(4)
	if _, err := ResolveExpansion(ExpansionInputs{Spec: spec, Plan: expanded}); err == nil {
		t.Fatal("expansion changed existing capacity")
	}
	plan.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Capacity: new(3)}}
	if _, err := ResolvePlan(PlanInputs{Declarations: declarations, Plan: plan}); err != nil {
		t.Fatalf("independent job and plan capacities refused: %v", err)
	}
}

func TestConcurrencyTimeoutsMustFitDuration(t *testing.T) {
	for _, timeout := range []int64{math.MaxInt64, int64(math.MaxInt64/time.Millisecond) + 1} {
		declaration := nodeproto.ConcurrencySpec{Group: "db", QueueTimeoutMS: &timeout}
		if _, err := ResolveConcurrency([]nodeproto.ConcurrencySpec{declaration}); err == nil {
			t.Errorf("accepted overflowing timeout: %+v", declaration)
		}
	}
	bound := int64(math.MaxInt64 / time.Millisecond)
	if _, err := ResolveConcurrency([]nodeproto.ConcurrencySpec{{Group: "db", QueueTimeoutMS: &bound}}); err != nil {
		t.Fatal(err)
	}
}
