package runners

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type hostingOwnerState struct {
	nodehost.NativeHostStore
	snapshot store.RunSnapshot
}

func (s hostingOwnerState) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return s.snapshot, nil
}

type hostingOwnerEngines struct{ source.EngineResolver }

func (hostingOwnerEngines) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine acquisition")
}

func TestHostingLeavesRenewalWithNativeOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		unit, principal, original := hostingStateFor(t, capability.UnitTrigger)
		spec := runspec.Spec{Request: unit.Request, Hash: "spec", PlanHash: "plan"}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: []byte(`{"jobs":[]}`), PlanHash: "plan"}
		state := hostingOwnerState{snapshot: original.snapshot}
		state.snapshot.Run.State = store.RunRunning
		state.snapshot.Run.Spec, state.snapshot.Run.Plan, state.snapshot.Run.PlanHash = &spec, unit.Accepted.Plan, "plan"
		s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
		authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: s, Bindings: s})
		if err != nil {
			t.Fatal(err)
		}
		renewals, confirmations, closed := 0, 0, false
		host := Hosting{
			ControllerURL: "https://controller.example",
			NewController: hostingClient(func(context.Context) (lease.Beat, error) { confirmations++; return lease.Beat{}, nil }),
			Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (NativeServices, capability.Principal, error) {
				return NativeServices{Config: nodehost.NativeHostConfig{
					Store: state, Lease: lease.Keeper{Lease: time.Minute, Interval: time.Second}, Source: source.Options{Engines: hostingOwnerEngines{}},
					Masks:     nodehost.NewMaskRegistry(secrets.NewMasker()),
					Router:    controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities}),
					Processes: &nodehost.NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},
					Planner:   nodehost.SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
					Executor: nodehost.ExecutorDependencies{Bind: func(context.Context, nodehost.NodeInput) (nodehost.ProcessGrant, error) {
						return nodehost.ProcessGrant{}, errors.New("unexpected execution")
					}},
					Evaluation: nodehost.EvaluationDependencies{Bind: func(context.Context, nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
						return nodehost.ProcessGrant{}, errors.New("unexpected evaluation")
					}},
					Owner: nodehost.OwnerHost{
						Renew: func(_ context.Context, actor store.Actor, computing bool) (store.LeaseBeat, error) {
							renewals++
							if actor.Authority != principal.Authority || computing != (renewals == 1) {
								t.Error("owner renewal lost authority or current activity")
							}
							if renewals == 2 {
								return store.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld}
							}
							return store.LeaseBeat{}, nil
						},
						Acquire: func(context.Context, nodehost.RunInput) (*source.Entry, error) { return &source.Entry{}, nil },
						Decide:  func(store.ScheduleSnapshot) store.ScheduleDecision { return store.ScheduleDecision{} },
						Wait:    func(ctx context.Context, _ store.RunSnapshot) error { <-ctx.Done(); return context.Cause(ctx) },
					},
				}, Close: func() error { closed = true; return authorities.Close() }}, principal, nil
			},
		}
		if err := host.Host(t.Context(), unit); !errors.Is(err, lease.ErrLeaseLost) || renewals != 2 || confirmations != 1 || !closed {
			t.Fatalf("renewals=%d confirmations=%d closed=%v: %v", renewals, confirmations, closed, err)
		}
	})
}
