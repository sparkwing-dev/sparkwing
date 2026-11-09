package sparkwingtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestGoConcurrencyCapacityMustBeExplicit(t *testing.T) {
	for _, capacity := range []int{0, 1} {
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).Concurrency(sparkwing.NewConcurrencyGroup("deploys", sparkwing.ConcurrencyLimit{Capacity: capacity}))
		raw, err := json.Marshal(sdkserve.DefaultOperations.Document(plan))
		if err != nil {
			t.Fatal(err)
		}
		_, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()})
		if capacity == 0 {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
				t.Fatalf("zero capacity = %v, want plan_invalid", err)
			}
		} else if err != nil {
			t.Fatalf("capacity %d: %v", capacity, err)
		}
	}
}

func TestHostRefusesUnknownConcurrencyPolicy(t *testing.T) {
	for _, limit := range []sparkwing.ConcurrencyLimit{{Capacity: 1, OnLimit: "qeue"}, {Capacity: 1, Scope: "globl"}} {
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).Concurrency(sparkwing.NewConcurrencyGroup("g", limit))
		raw, err := json.Marshal(sdkserve.DefaultOperations.Document(plan))
		if err != nil {
			t.Fatal(err)
		}
		_, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
			t.Fatalf("%+v = %v, want plan_invalid", limit, err)
		}
	}
}

func TestGeneratedSettingsReachHostValidation(t *testing.T) {
	p := sparkwing.NewPlan()
	n := sparkwing.Job(p, "source", func(context.Context) error { return nil })
	g := n.Generate(func(_ context.Context, p *sparkwing.Plan) error { p.Priority(0); return nil })
	result, err := sdkserve.DefaultOperations.Eval(t.Context(), p, nodewire.Request{Job: n.ID(), Closure: g.ID()})
	if err != nil || result.Error != nil {
		t.Fatalf("generation=%+v error=%v", result, err)
	}
	if _, err := nodeproto.ValidatePlan(result.Value, nodeproto.ValidationContext{Expansion: true, Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}); err == nil {
		t.Fatal("host accepted generated settings")
	}
}

func TestNegativeDurationDoesNotBecomeZero(t *testing.T) {
	p := sparkwing.NewPlan()
	sparkwing.Job(p, "job", func(context.Context) error { return nil }).Timeout(-time.Nanosecond)
	raw, err := json.Marshal(sdkserve.DefaultOperations.Document(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}); err == nil {
		t.Fatal("negative duration became an allowed zero")
	}
}
