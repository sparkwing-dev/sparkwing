package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
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
)

type cliOwnerStore struct {
	nodehost.NativeHostStore
	mu       sync.Mutex
	snapshot store.RunSnapshot
	actor    store.Actor
	renewed  int
	finished *store.FinishRunRequest
	failure  error
}

func (s *cliOwnerStore) RenewOwner(_ context.Context, actor store.Actor) (store.LeaseBeat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor != s.actor {
		return store.LeaseBeat{}, errors.New("renewal changed the awarded owner")
	}
	s.renewed++
	return store.LeaseBeat{}, nil
}

func (s *cliOwnerStore) ReadSnapshot(_ context.Context, run string) (store.RunSnapshot, error) {
	if run != s.snapshot.Run.ID {
		return store.RunSnapshot{}, errors.New("read changed the awarded run")
	}
	return s.snapshot, nil
}

func (s *cliOwnerStore) FinishRunAndUnit(_ context.Context, finish store.FinishRunRequest) (store.Applied, error) {
	if finish.Transition.Actor != s.actor || finish.Unit != s.actor.Owner || finish.Transition.RunID != s.snapshot.Run.ID {
		return store.Applied{}, errors.New("completion changed the awarded owner")
	}
	s.finished = &finish
	return store.Applied{}, s.failure
}

func TestCLIOwnerCompositionPreservesRetrySeals(t *testing.T) {
	for _, here := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			for _, failCompletion := range []bool{false, true} {
				t.Run(fmt.Sprintf("here=%t/retry=%t/failed-commit=%t", here, retry, failCompletion), func(t *testing.T) {
					award := localTestAward(t)
					if here {
						award = hereAward(t)
					}
					award.RunID = award.Request.RunID
					if retry {
						award.RunID = "retry-of-" + award.Request.RunID
					}
					award.Binding.RunID = award.RunID
					plan := json.RawMessage(`{"jobs":[]}`)
					spec, err := runspec.SealSpec(runspec.SealInput{
						Request: award.Request, Plan: plan,
						Resolved: runspec.Resolved{SecretArgs: []string{"password"}, Args: award.Request.Request.RequestedArgs},
					})
					if err != nil {
						t.Fatal(err)
					}
					award.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: plan, PlanHash: spec.PlanHash}
					actor := store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{
						Kind: store.OwnerTrigger, RunID: award.RunID,
						Holder: award.Binding.Holder, Generation: award.Binding.Generation,
					}}
					state := &cliOwnerStore{actor: actor, snapshot: store.RunSnapshot{Run: store.RunRow{
						ID: award.RunID, State: store.RunRunning, Request: award.Request, Spec: &spec,
						Plan: plan, PlanHash: string(spec.PlanHash), Owner: &actor.Owner,
					}}}
					if failCompletion {
						state.failure = errors.New("completion was not committed")
					}
					acquired, closed := 0, 0
					owner := nodehost.OwnerHost{
						Store: state, Masker: secrets.NewMasker(),
						Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
						Wait:  func(context.Context, store.RunSnapshot) error { return errors.New("unexpected wait") },
						Acquire: func(_ context.Context, input nodehost.RunInput) (*source.Entry, error) {
							acquired++
							var request runspec.Request
							if err := json.Unmarshal(input.SealedRequest, &request); err != nil || !reflect.DeepEqual(request, award.Request.Request) || input.RunID != award.RunID {
								t.Fatal("acquisition lost the original request or current execution identity")
							}
							return &source.Entry{}, nil
						},
					}
					transport := hostStateController{
						renew: func(ctx context.Context, unit string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
							if unit != award.UnitID || !body.Executing {
								t.Fatal("preparation changed its award or omitted compute")
							}
							_, err := state.RenewOwner(ctx, state.actor)
							return controllerwire.RenewalResponse{}, err
						},
						finish: func(ctx context.Context, run string, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
							_, err := state.FinishRunAndUnit(ctx, store.FinishRunRequest{
								Unit: state.actor.Owner, Transition: store.RunTransition{
									RunID: run, To: store.RunState(body.Outcome), Actor: state.actor, Cause: store.RunFinish, Reason: body.Reason,
								},
							})
							return controllerwire.Applied{}, err
						},
					}
					adapter, err := nodehost.NewRemoteHostStore(transport, state, award.UnitID, func() bool { return false })
					if err != nil {
						t.Fatal(err)
					}
					owner.Renew = adapter.RenewOwner
					host := cliNativeOwner(t, adapter, owner)
					bind := func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error) {
						return hereHostSession{host: host, input: nodehost.RunInput{Owner: store.Actor{Kind: store.ActorController, Owner: store.OwnerLeaseRef{RunID: "foreign-run"}}}, close: func() error { closed++; return nil }}, nil
					}
					var completion nodehost.RunCompletion
					if here {
						completion, err = runHereAward(t.Context(), client.EngineOptions{}, award.RunID, award,
							func(ctx context.Context, _ client.EngineOptions, unit controllerwire.ClaimedUnit) (hereHostSession, error) {
								return bind(ctx, unit)
							})
					} else {
						runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
							return controllerwire.ClaimResponse{Claim: &award}, nil
						}}}
						completion, err = hostLocalRun(t.Context(), runtime, award.RunID, award.Request.Hash, "v1.2.3")
					}
					if failCompletion {
						if !errors.Is(err, state.failure) || completion.Outcome != "" {
							t.Fatalf("uncommitted completion = %+v, %v", completion, err)
						}
					} else if err != nil || completion.Outcome != "success" {
						t.Fatalf("completion = %+v, %v", completion, err)
					}
					state.mu.Lock()
					defer state.mu.Unlock()
					if acquired != 1 || closed != 1 || state.renewed == 0 || state.finished == nil || state.finished.Transition.To != store.RunSuccess {
						t.Fatalf("acquired=%d closed=%d renewals=%d finish=%+v", acquired, closed, state.renewed, state.finished)
					}
				})
			}
		}
	}
}

type cliNativeBoundaries struct{}

func (cliNativeBoundaries) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine resolution")
}

func (cliNativeBoundaries) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine verification")
}

func (cliNativeBoundaries) ReadAuthentication(context.Context, capability.CredentialLookup, capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	return capability.AuthenticationSnapshot{}, errors.New("unexpected process authentication")
}

func (cliNativeBoundaries) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return errors.New("unexpected process binding")
}

func cliNativeOwner(t *testing.T, state nodehost.NativeHostStore, owner nodehost.OwnerHost) *nodehost.OwnerHost {
	t.Helper()
	boundary := cliNativeBoundaries{}
	authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: boundary, Bindings: boundary})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := authorities.Close(); err != nil {
			t.Error(err)
		}
	})
	router := controller.NewPolicyRouter(controller.RouteAuthorization{})
	host, _, _, err := nodehost.NewNativeHost(nodehost.NativeHostConfig{
		Store: state, Lease: owner.Lease, Owner: owner,
		Source: source.Options{Engines: boundary}, Masks: nodehost.NewMaskRegistry(secrets.NewMasker()), Router: router,
		Processes: &nodehost.NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error {
			return errors.New("unexpected execution log")
		}},
		Planner: nodehost.SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return errors.New("unexpected planning log") }},
		Executor: nodehost.ExecutorDependencies{Bind: func(context.Context, nodehost.NodeInput) (nodehost.ProcessGrant, error) {
			return nodehost.ProcessGrant{}, errors.New("unexpected execution delegation")
		}},
		Evaluation: nodehost.EvaluationDependencies{
			Bind: func(context.Context, nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
				return nodehost.ProcessGrant{}, errors.New("unexpected evaluation delegation")
			},
			ReadOutput: func(context.Context, controllerwire.NodeRef, int64) (json.RawMessage, error) {
				return nil, errors.New("unexpected output read")
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(router.Registrations()) == 0 {
		t.Fatal("native host registered no process routes")
	}
	return host
}
