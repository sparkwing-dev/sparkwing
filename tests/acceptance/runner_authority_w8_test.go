package acceptance_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRunnerAuthorityW8Components(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     capability.UnitKind
		purpose  capability.AwardPurpose
		delivery capability.DeliveryPurpose
		allowed  bool
	}{
		{"trigger", capability.UnitTrigger, capability.PurposeTrigger, capability.DeliveryDirect, true},
		{"here", capability.UnitTrigger, capability.PurposeHere, capability.DeliveryDirect, true},
		{"work", capability.UnitNode, capability.PurposeWork, capability.DeliveryDirect, true},
		{"bootstrap", capability.UnitNode, capability.PurposeWork, capability.DeliveryBootstrap, false},
		{"missing_delivery", capability.UnitNode, capability.PurposeWork, "", false},
		{"missing_purpose", capability.UnitNode, "", capability.DeliveryDirect, false},
		{"eval_as_work", capability.UnitNode, capability.PurposeEval, capability.DeliveryDirect, false},
		{"work_as_trigger", capability.UnitTrigger, capability.PurposeWork, capability.DeliveryDirect, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := runnerAuthorityAwardW8(tc.kind)
			unit.Purpose, unit.Delivery = tc.purpose, tc.delivery
			assertRunnerAuthorityW8(t, unit, tc.allowed)
		})
	}
	t.Run("here_reclaim_after_initial_claim_window", func(t *testing.T) {
		unit := runnerAuthorityAwardW8(capability.UnitTrigger)
		unit.Purpose = capability.PurposeHere
		claimedBy := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		unit.Binding.ClaimBy = &claimedBy
		assertRunnerAuthorityW8(t, unit, true)
	})
}

func TestRunnerDeadlineW8Components(t *testing.T) {
	for _, bound := range []string{"binding", "owner", "parent_credential"} {
		t.Run(bound+"_expired", func(t *testing.T) {
			unit := runnerAuthorityAwardW8(capability.UnitNode)
			expired := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			setRunnerDeadlineW8(&unit, bound, &expired)
			assertRunnerAuthorityW8(t, unit, false)
		})
	}
	for _, bound := range []string{"binding", "owner", "parent_credential", "caller"} {
		t.Run(bound+"_cancels_binding", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first, later := time.Now().Add(time.Second), time.Now().Add(time.Hour)
				unit := runnerAuthorityAwardW8(capability.UnitNode)
				unit.Binding.Deadline, unit.OwnerDeadline, unit.ParentCapabilityExpiry = &later, &later, &later
				ctx := t.Context()
				if bound == "caller" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, first)
					defer cancel()
				} else {
					setRunnerDeadlineW8(&unit, bound, &first)
				}
				boundNative, closed := false, false
				host := runners.Hosting{ControllerURL: "https://controller.example", NewController: runnerControllerFactoryW8(func(ctx context.Context) (lease.Beat, error) {
					if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(first) {
						t.Fatal("renewal lost the earliest deadline")
					}
					return lease.Beat{}, nil
				}), Bind: func(ctx context.Context, _ controllerwire.ClaimedUnit, _ controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
					boundNative = true
					<-ctx.Done()
					return runners.NativeServices{Close: func() error { closed = true; return nil }}, capability.Principal{}, context.Cause(ctx)
				}}
				if err := host.Host(ctx, unit); !errors.Is(err, context.DeadlineExceeded) || !boundNative || !closed {
					t.Fatalf("bound=%v closed=%v error=%v", boundNative, closed, err)
				}
			})
		})
	}
}

func setRunnerDeadlineW8(unit *controllerwire.ClaimedUnit, bound string, deadline *time.Time) {
	switch bound {
	case "binding":
		unit.Binding.Deadline = deadline
	case "owner":
		unit.OwnerDeadline = deadline
	case "parent_credential":
		unit.ParentCapabilityExpiry = deadline
	}
}

// safety: only direct execution awards with valid fences and unexpired authority reach preparation.
func FuzzRunnerAuthorityW8(f *testing.F) {
	for _, seed := range [][3]string{
		{"trigger", "trigger", "direct"},
		{"trigger", "here", "direct"},
		{"node", "work", "direct"},
		{"node", "work", "bootstrap"},
		{"node", "eval", "direct"},
		{"eval", "eval", "direct"},
		{"node", "", "direct"},
		{"node", "work", ""},
	} {
		for _, bounds := range []uint8{0, 1, 4, 16, 2, 8, 32, 63} {
			f.Add(seed[0], seed[1], seed[2], int64(1), int64(0), bounds)
		}
	}
	allowed := map[[3]string]bool{
		{"trigger", "trigger", "direct"}: true,
		{"trigger", "here", "direct"}:    true,
		{"node", "work", "direct"}:       true,
	}
	f.Fuzz(func(t *testing.T, kind, purpose, delivery string, generation, attempt int64, bounds uint8) {
		unit := runnerAuthorityAwardW8(capability.UnitKind(kind))
		unit.Purpose, unit.Delivery = capability.AwardPurpose(purpose), capability.DeliveryPurpose(delivery)
		unit.Binding.Generation = generation
		want := allowed[[3]string{kind, purpose, delivery}] && generation > 0
		if kind == "node" {
			unit.Binding.Attempt = attempt
			want = want && attempt >= 0
		}
		for _, bound := range []string{"binding", "owner", "parent_credential"} {
			var deadline *time.Time
			switch bounds & 3 {
			case 1:
				value := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
				deadline, want = &value, false
			case 2:
				deadline, want = new(time.Time), false
			case 3:
				value := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
				deadline = &value
			}
			setRunnerDeadlineW8(&unit, bound, deadline)
			bounds >>= 2
		}
		assertRunnerAuthorityW8(t, unit, want)
	})
}

func runnerAuthorityAwardW8(kind capability.UnitKind) controllerwire.ClaimedUnit {
	unit := controllerwire.ClaimedUnit{
		Unit: kind, UnitID: "unit", RunID: "run", Capability: "private-award",
		Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect,
		Binding:        capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "holder", Generation: 1},
		LeaseExpiresAt: time.Now().Add(time.Minute),
		Request:        runspec.SealedRequest{RunID: "run", Hash: "request-hash", Request: runspec.Request{Team: "team"}},
	}
	if kind == capability.UnitNode {
		unit.Purpose, unit.JobID = capability.PurposeWork, "job"
		unit.Binding.Kind, unit.Binding.NodeID = capability.BindingNode, "job"
		bindRunnerIdentityW8(&unit)
	}
	return unit
}

func assertRunnerAuthorityW8(t *testing.T, unit controllerwire.ClaimedUnit, allowed bool) {
	t.Helper()
	reached := errors.New("native binding reached")
	renewed, bound := false, false
	host := runners.Hosting{
		ControllerURL: "https://controller.example", NewController: runnerControllerFactoryW8(func(context.Context) (lease.Beat, error) { renewed = true; return lease.Beat{}, nil }),
		Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
			bound = true
			return runners.NativeServices{}, capability.Principal{}, reached
		},
	}
	err := host.Host(t.Context(), unit)
	if allowed {
		if !errors.Is(err, reached) || !renewed || !bound {
			t.Fatalf("direct award: renew=%v bind=%v error=%v", renewed, bound, err)
		}
	} else if err == nil || renewed || bound {
		t.Fatalf("invalid award: renew=%v bind=%v error=%v", renewed, bound, err)
	}
}
