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

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cliOwnerStore struct {
	nodehost.OwnerStore
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
						Kind: store.OwnerTrigger, Name: award.UnitID, RunID: award.RunID,
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
					host := &nodehost.OwnerHost{
						Store: state, Mask: func(s string) string { return s },
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
					bind := func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error) {
						return hereHostSession{host: host, input: nodehost.RunInput{Owner: actor}, close: func() error { closed++; return nil }}, nil
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
