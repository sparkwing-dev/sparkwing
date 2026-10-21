package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGeneratorReplay(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"cross_group", "cross_source", "nested", "recovery_ancestry", "duplicate_source", "dropped_peer", "changed_peer", "wrong_group", "unknown_field", "conflicting_source", "conflicting_output", "invalid_output", "undeclared_generator"} {
		t.Run(mode, func(t *testing.T) {
			var trace []string
			var processes []*scriptedSDK
			input, persisted, _ := executionHostFixture(t, "success", &trace, &processes)
			input.RouteBinding.Routes.Bearer = ""
			job := input.Accepted.Plan.Jobs[0]
			job.Groups, job.Needs = []string{"target"}, []string{"peer"}
			crossSource := mode == "cross_source" || mode == "recovery_ancestry"
			if mode == "recovery_ancestry" {
				job.OnFailureOf = "peer"
			}
			peer := nodeproto.JobSpec{ID: "peer", Groups: []string{"peers"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
			sourceJob := nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"target", "peers", "empty"}}}
			if crossSource {
				sourceJob.Closures.Generate = []string{"target", "empty"}
			}
			input.Accepted.Plan = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{sourceJob}}
			if crossSource {
				input.Accepted.Plan.Jobs = append(input.Accepted.Plan.Jobs, nodeproto.JobSpec{ID: "other-source", Closures: nodeproto.ClosureSpec{Generate: []string{"peers"}}})
			}
			if mode == "nested" {
				sourceJob.Groups = []string{"ancestors"}
				input.Accepted.Plan.Jobs = []nodeproto.JobSpec{{ID: "root", Closures: nodeproto.ClosureSpec{Generate: []string{"ancestors"}}}}
			}
			planHash, _ := expansionHash(input.Accepted.Plan)
			input.Accepted.PlanHash = string(planHash)
			persisted.snapshot.Run.Spec.PlanHash = planHash
			input.Accepted.Spec, _ = json.Marshal(persisted.snapshot.Run.Spec)
			persisted.snapshot.Run.Plan, _ = json.Marshal(input.Accepted.Plan)
			persisted.snapshot.Run.PlanHash = string(planHash)
			jobHash, _ := expansionHash(job)
			input.SpecHash = string(jobHash)
			persisted.snapshot.Nodes[0].SpecHash = input.SpecHash
			persisted.snapshot.Nodes[0].Spec, _ = json.Marshal(job)
			accepted := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job, peer}}
			if crossSource {
				accepted.Jobs = accepted.Jobs[:1]
			}
			body, _ := json.Marshal(accepted)
			bodyHash, _ := expansionHash(accepted)
			persisted.snapshot.Expansions = []store.AcceptedExpansionRow{{Source: controllerwire.NodeRef{RunID: "run", NodeID: "source"}, SourceGeneration: 0, Body: body, BodyHash: string(bodyHash)}}
			peerHash, _ := expansionHash(peer)
			peerBody, _ := json.Marshal(peer)
			s := persisted
			s.nodes = map[string]store.NodeRow{"peer": {Ref: controllerwire.NodeRef{RunID: "run", NodeID: "peer"}, Spec: peerBody, SpecHash: string(peerHash)}}
			read := s.readNode
			s.readNode = func(ctx context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
				if row, ok := s.nodes[ref.NodeID]; ok && row.Ref == ref {
					return row, nil
				}
				return read(ctx, ref)
			}
			input.Replay = []GeneratorReplay{{SourceJob: "source", SourceGeneration: 0, GeneratorID: "target", Output: json.RawMessage(`{"value":7}`)}}
			if crossSource {
				body := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{peer}}
				raw, _ := json.Marshal(body)
				hash, _ := expansionHash(body)
				persisted.snapshot.Expansions = append(persisted.snapshot.Expansions, store.AcceptedExpansionRow{Source: controllerwire.NodeRef{RunID: "run", NodeID: "other-source"}, SourceGeneration: 2, Body: raw, BodyHash: string(hash)})
				persisted.snapshot.Nodes = append(persisted.snapshot.Nodes, s.nodes["peer"])
				input.Replay = append(input.Replay, GeneratorReplay{SourceJob: "other-source", SourceGeneration: 2, GeneratorID: "peers", Output: json.RawMessage(`{"value":7}`)})
			}
			if mode == "nested" {
				expansion := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{sourceJob}}
				raw, _ := json.Marshal(expansion)
				hash, _ := expansionHash(expansion)
				persisted.snapshot.Expansions = append(persisted.snapshot.Expansions, store.AcceptedExpansionRow{Source: controllerwire.NodeRef{RunID: "run", NodeID: "root"}, SourceGeneration: 2, Body: raw, BodyHash: string(hash)})
				raw, _ = json.Marshal(sourceJob)
				hash, _ = expansionHash(sourceJob)
				s.nodes["source"] = store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "source"}, Spec: raw, SpecHash: string(hash)}
				persisted.snapshot.Nodes = append(persisted.snapshot.Nodes, s.nodes["source"], s.nodes["peer"])
				input.Replay = append([]GeneratorReplay{{SourceJob: "root", SourceGeneration: 2, GeneratorID: "ancestors", Output: json.RawMessage(`{"value":7}`)}}, input.Replay...)
			}
			var recovery *nodeproto.JobFailure
			if mode == "recovery_ancestry" {
				row := s.nodes["peer"]
				row.Status, row.Outcome = store.StatusDone, store.NodeFailed
				s.nodes["peer"] = row
				recovery = &nodeproto.JobFailure{Job: "peer", Stage: "action", Error: &apierr.Error{Code: apierr.SDKError, Message: "recorded failure"}}
			}
			if mode == "conflicting_source" {
				input.Replay = append(input.Replay, GeneratorReplay{SourceJob: "source", SourceGeneration: 1, GeneratorID: "peers", Output: json.RawMessage(`{"value":7}`)})
			}
			if mode == "duplicate_source" || mode == "conflicting_output" {
				replay := input.Replay[0]
				replay.GeneratorID = "peers"
				if mode == "conflicting_output" {
					replay.Output = json.RawMessage(`{"value":8}`)
				}
				input.Replay = append(input.Replay, replay)
			}
			if mode == "invalid_output" {
				input.Replay[0].Output = nil
			}
			if mode == "undeclared_generator" {
				input.Replay[0].GeneratorID = "undeclared"
			}
			var calls []string
			sdk := newExecutionSDK(input.Accepted.Plan, input, "success", &trace)
			process := replaySDK(sdk, recovery, func(request nodeproto.Request) (json.RawMessage, error) {
				calls = append(calls, request.Closure)
				if request.Job != "" || string(request.Input) != `{"value":7}` {
					return nil, errors.New("replay lost recorded source output")
				}
				jobs := []nodeproto.JobSpec{}
				switch request.Closure {
				case "ancestors":
					jobs = append(jobs, sourceJob)
				case "target":
					jobs = append(jobs, job)
				case "peers":
					if mode != "dropped_peer" {
						if mode == "changed_peer" {
							peer.Optional = true
						}
						if mode == "wrong_group" {
							peer.Groups = []string{"target"}
						}
						if mode == "unknown_field" {
							return json.RawMessage(`[{"id":"peer","groups":["peers"],"unknown":true}]`), nil
						}
						jobs = append(jobs, peer)
					}
				case "empty":
				default:
					return nil, errors.New("unexpected generator")
				}
				return json.Marshal(jobs)
			})
			validated := 0
			executor, err := NewNodeExecutor(ExecutorDependencies{
				Store: s, Start: func(_ context.Context, in NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
					sdk.input = in
					return process, nil
				},
				EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
					return input.Engine, nil
				},
				CanonicalHash: expansionHash, Mask: func(text string) string { return text },
				ReadFailure: func(_ context.Context, ref controllerwire.NodeRef, generation int64) (*nodeproto.JobFailure, error) {
					if recovery == nil || ref != s.nodes["peer"].Ref || runspec.ClaimGeneration(generation) != s.nodes["peer"].Generation {
						return nil, errors.New("undeclared recovery read")
					}
					return recovery, nil
				},
				ChildContext: func(ctx context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
					child, cancel := context.WithCancel(ctx)
					return child, cancel, nil
				},
				ValidatePlan: func(raw json.RawMessage, validation nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					var plan nodeproto.PlanDoc
					decoder := json.NewDecoder(bytes.NewReader(raw))
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&plan); err != nil {
						return plan, err
					}
					if !validation.Expansion {
						return plan, nil
					}
					validated++
					known := map[string]bool{}
					for _, job := range append(append([]nodeproto.JobSpec{}, validation.ExistingJobs...), plan.Jobs...) {
						known[job.ID] = true
					}
					for _, job := range plan.Jobs {
						for _, need := range job.Needs {
							if !known[need] {
								return plan, &apierr.Error{Code: apierr.PlanInvalid, Message: "dependency absent from complete expansion"}
							}
						}
					}
					return plan, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			input.Entry = &source.Entry{Engine: input.Engine}
			before, _ := json.Marshal(s.snapshot)
			err = executor.Execute(t.Context(), input)
			ok := mode == "cross_group" || mode == "duplicate_source" || mode == "nested" || crossSource
			if ok {
				want, validations := []string{"target", "peers", "empty"}, 1
				if crossSource {
					want, validations = []string{"target", "empty", "peers"}, 2
				}
				if mode == "nested" {
					want, validations = []string{"ancestors", "target", "peers", "empty"}, 2
				}
				if err != nil || validated != validations || !reflect.DeepEqual(calls, want) {
					t.Fatalf("replay calls=%v validations=%d error=%v", calls, validated, err)
				}
			} else if err == nil {
				t.Fatal("changed expansion reached execution")
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.PlanDrift {
					t.Fatalf("changed expansion returned %v, want plan_drift", err)
				}
			}
			for _, record := range trace {
				if !ok && record == "body:1" {
					t.Fatal("changed expansion reached the body")
				}
			}
			if !sdk.closed || !sdk.cleaned {
				t.Fatal("replay did not join the SDK process")
			}
			after, _ := json.Marshal(s.snapshot)
			if !bytes.Equal(before, after) {
				t.Fatal("replay changed persisted topology")
			}
		})
	}
}
