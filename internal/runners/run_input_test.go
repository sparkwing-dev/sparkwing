package runners

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func FuzzHostingNodeInputKeepsAwardSeals(f *testing.F) {
	f.Add("other-run", "other-hash", int64(999))
	f.Add("run", "spec-hash", int64(0))
	f.Fuzz(func(t *testing.T, preparedRun, preparedHash string, retry int64) {
		unit := hostingAward(capability.UnitNode)
		unit.Accepted.Spec.Hash, unit.Accepted.PlanHash = "spec-hash", "plan-hash"
		unit.Accepted.Spec.Request.Request.RequestedArgs = map[string]string{"key": "sealed"}
		unit.Accepted.Plan = json.RawMessage(`{"jobs":[{"id":"job"}]}`)
		wait := int64(100)
		unit.Execution = &runspec.JobExecutionSettings{
			Retry:       runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 2}},
			Requires:    runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"sealed"}}},
			Concurrency: []runspec.Concurrency{{Group: "sealed", Capacity: 3, Cost: 1, QueueTimeoutMS: &wait}},
		}
		wantSpec, err := json.Marshal(unit.Accepted.Spec)
		if err != nil {
			t.Fatal(err)
		}
		wantExecution, err := json.Marshal(unit.Execution)
		if err != nil {
			t.Fatal(err)
		}
		prepared := hostingInput(unit)
		prepared.Accepted = nodehost.AcceptedRun{RunID: preparedRun, SpecHash: preparedHash}
		prepared.Execution.Retry = runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: int(retry)}}
		executed := false
		host := Hosting{
			ControllerURL: "http://controller", Lease: time.Minute, Interval: time.Second,
			NewController: hostingClient(func(context.Context) (lease.Beat, error) {
				unit.Accepted.Spec.Request.Request.RequestedArgs["key"] = "changed"
				unit.Accepted.Plan[0] = '!'
				unit.Execution.Requires.Value[0] = "changed"
				unit.Execution.Concurrency[0].Cost = 9
				wait = 900
				return lease.Beat{}, nil
			}),
			StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
			PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				return nodeHostFunc(func(_ context.Context, input nodehost.NodeInput) error {
					executed = true
					accepted := input.Accepted
					if accepted.RunID != "run" || accepted.SpecHash != "spec-hash" || accepted.PlanHash != "plan-hash" || string(accepted.Spec) != string(wantSpec) || len(accepted.Plan.Jobs) != 1 || accepted.Plan.Jobs[0].ID != "job" {
						t.Fatal("node execution replaced the awarded seals")
					}
					gotExecution, err := json.Marshal(input.Execution)
					if err != nil || string(gotExecution) != string(wantExecution) {
						t.Fatal("node execution replaced the awarded settings")
					}
					input.Execution.Requires.Value[0] = "executor"
					*input.Execution.Concurrency[0].QueueTimeoutMS = 7
					return nil
				}), prepared, nil, nil
			},
		}
		if err := host.Host(t.Context(), unit); err != nil || !executed {
			t.Fatalf("node seals: executed=%v error=%v", executed, err)
		}
		if unit.Execution.Requires.Value[0] != "changed" || wait != 900 {
			t.Fatal("executor mutated the award's settings")
		}
	})
}

func FuzzHostingRunInputKeepsAwardSeals(f *testing.F) {
	f.Add("other-run", "other-hash", false, false)
	f.Add("run", "request-hash", true, true)
	f.Fuzz(func(t *testing.T, preparedRun, preparedHash string, resume, here bool) {
		unit := hostingAward(capability.UnitTrigger)
		if here {
			unit.Purpose = capability.PurposeHere
		}
		unit.Request.Hash = "request-hash"
		unit.Request.Request.RequestedArgs = map[string]string{"key": "sealed"}
		unit.Runner.Labels = []string{"sealed-label"}
		if resume {
			unit.Accepted = &controllerwire.PlanAccepted{
				Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash"},
				Plan: json.RawMessage(`{"jobs":[{"id":"sealed-job"}]}`), PlanHash: "plan-hash",
			}
		}
		executed, joined, closed := false, false, false
		host := Hosting{
			ControllerURL: "http://controller", Lease: time.Minute, Interval: time.Second,
			NewController: hostingClient(func(context.Context) (lease.Beat, error) {
				unit.Request.Request.RequestedArgs["key"] = "changed-after-award"
				unit.Runner.Labels[0] = "changed-after-award"
				if unit.Accepted != nil {
					unit.Accepted.Plan[0] = '!'
				}
				return lease.Beat{}, nil
			}),
			StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
				return ctx, func() { joined = true }
			},
			PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				prepared := nodehost.RunInput{RunID: preparedRun, RequestHash: preparedHash, SealedRequest: json.RawMessage(`{"ambient":true}`), Accepted: &nodehost.AcceptedRun{RunID: preparedRun}, GrantedRunner: nodeproto.RunnerIdentity{Name: preparedRun, Type: "forged", Labels: []string{preparedHash}}, Owner: store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "authenticated-prefix"}, CapabilityPrefix: "authenticated-prefix", Kind: store.ActorOperator, Owner: store.OwnerLeaseRef{RunID: preparedRun, Holder: preparedHash, Generation: 999}}}
				return runHostFunc(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						executed = true
						var request runspec.Request
						if err := json.Unmarshal(input.SealedRequest, &request); err != nil {
							t.Fatal(err)
						}
						if input.RunID != "run" || input.RequestHash != "request-hash" || request.RequestedArgs["key"] != "sealed" {
							t.Fatal("run execution lost the awarded first seal")
						}
						if input.GrantedRunner.Name != "granted-runner" || input.GrantedRunner.Type != "runner" || len(input.GrantedRunner.Labels) != 1 || input.GrantedRunner.Labels[0] != "sealed-label" {
							t.Fatal("run execution replaced the granted runner")
						}
						if input.Owner.Kind != store.ActorRunHost || input.Owner.Owner != (store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Holder: "holder", Generation: 2}) || input.Owner.Authority.ID != "authenticated-prefix" || input.Owner.CapabilityPrefix != "authenticated-prefix" {
							t.Fatal("run execution replaced the owner fence or authenticated authority")
						}
						if resume {
							accepted := input.Accepted
							if accepted == nil || accepted.RunID != "run" || accepted.SpecHash != "spec-hash" || accepted.PlanHash != "plan-hash" || len(accepted.Plan.Jobs) != 1 || accepted.Plan.Jobs[0].ID != "sealed-job" {
								t.Fatal("run resume lost the awarded second seal")
							}
							var spec runspec.Spec
							if err := json.Unmarshal(accepted.Spec, &spec); err != nil || spec.Request.Request.RequestedArgs["key"] != "sealed" {
								t.Fatal("run resume reread mutable award data")
							}
						} else if input.Accepted != nil {
							t.Fatal("new run inherited a prepared second seal")
						}
						return nodehost.RunCompletion{}, nil
					}), prepared, func() error {
						if !joined {
							t.Error("run services closed before joining the keeper")
						}
						closed = true
						return nil
					}, nil
			},
		}
		if err := host.Host(t.Context(), unit); err != nil || !executed || !joined || !closed {
			t.Fatalf("run composition: executed=%v joined=%v closed=%v error=%v", executed, joined, closed, err)
		}
	})
}

func TestHostingRefusesDifferentRunSealsBeforeRenewal(t *testing.T) {
	for name, change := range map[string]func(*controllerwire.ClaimedUnit){
		"request_run":   func(u *controllerwire.ClaimedUnit) { u.Request.RunID = "other" },
		"request_team":  func(u *controllerwire.ClaimedUnit) { u.Request.Request.Team = "" },
		"request_hash":  func(u *controllerwire.ClaimedUnit) { u.Request.Hash = "" },
		"accepted_run":  func(u *controllerwire.ClaimedUnit) { u.Accepted.Spec.Request.RunID = "other" },
		"accepted_team": func(u *controllerwire.ClaimedUnit) { u.Accepted.Spec.Request.Request.Team = "other" },
		"accepted_request_hash": func(u *controllerwire.ClaimedUnit) {
			u.Accepted.Spec.Request.Hash = "other"
		},
		"accepted_spec_hash": func(u *controllerwire.ClaimedUnit) { u.Accepted.Spec.Hash = "" },
		"accepted_plan_hash": func(u *controllerwire.ClaimedUnit) { u.Accepted.PlanHash = "" },
		"accepted_plan":      func(u *controllerwire.ClaimedUnit) { u.Accepted.Plan = json.RawMessage(`!`) },
	} {
		t.Run(name, func(t *testing.T) {
			unit := hostingAward(capability.UnitTrigger)
			unit.Accepted = &controllerwire.PlanAccepted{
				Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash"},
				Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: "plan-hash",
			}
			change(&unit)
			host := Hosting{
				ControllerURL: "http://controller", Lease: time.Minute, Interval: time.Second,
				NewController: hostingClient(func(context.Context) (lease.Beat, error) {
					t.Fatal("renewed an award with different run seals")
					return lease.Beat{}, nil
				}),
				PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
					t.Fatal("prepared an award with different run seals")
					return nil, nodehost.RunInput{}, nil, nil
				},
			}
			if err := host.Host(t.Context(), unit); err == nil {
				t.Fatal("accepted different run seals")
			}
		})
	}
}
