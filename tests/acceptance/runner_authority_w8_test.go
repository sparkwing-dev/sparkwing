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
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
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
		t.Run(bound+"_cancels_execution", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				first, later := now.Add(time.Second), now.Add(time.Hour)
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
				executed, joined, cleaned := false, false, false
				host := runners.Hosting{
					ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
					NewController: runnerControllerFactoryW8(func(ctx context.Context) (lease.Beat, error) {
						if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(first) {
							t.Fatalf("renewal deadline = %v, present=%v, want %v", deadline, ok, first)
						}
						return lease.Beat{}, nil
					}),
					StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
						return ctx, func() { joined = true }
					},
					PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
						return runnerDeadlineNodeW8(func(ctx context.Context) {
								executed = true
								<-ctx.Done()
							}), runnerIdentityInputW8(unit), func() error {
								if !joined {
									t.Error("source closed before keeper joined")
								}
								cleaned = true
								return nil
							}, nil
					},
				}
				if err := host.Host(ctx, unit); !errors.Is(err, context.DeadlineExceeded) || !executed || !joined || !cleaned {
					t.Fatalf("deadline result = %v, executed=%v, joined=%v, cleaned=%v", err, executed, joined, cleaned)
				}
			})
		})
	}
}

type runnerDeadlineNodeW8 func(context.Context)

func (run runnerDeadlineNodeW8) Execute(ctx context.Context, _ nodehost.NodeInput) error {
	run(ctx)
	return nil
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
	reached := errors.New("host preparation reached")
	renewed, prepared := false, false
	host := runners.Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: runnerControllerFactoryW8(func(context.Context) (lease.Beat, error) {
			renewed = true
			return lease.Beat{}, nil
		}),
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
		PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
			prepared = true
			return nil, nodehost.RunInput{}, nil, reached
		},
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			prepared = true
			return nil, nodehost.NodeInput{}, nil, reached
		},
	}
	err := host.Host(t.Context(), unit)
	if allowed {
		if !errors.Is(err, reached) || !renewed || !prepared {
			t.Fatalf("direct award never reached preparation: %v, renewed=%v, prepared=%v", err, renewed, prepared)
		}
	} else if err == nil || errors.Is(err, reached) || renewed || prepared {
		t.Fatalf("invalid award reached hosting: %v, renewed=%v, prepared=%v", err, renewed, prepared)
	}
}
