package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func strictExpansionJobs(mode, closure string) json.RawMessage {
	fields := map[string]string{
		"unknown":           `"unknown":true,`,
		"duplicate":         `"id":"shadow",`,
		"null":              `"deps":null,`,
		"concurrency":       `"concurrency":[{"group":"capacity","capacity":0,"cost":1}],`,
		"mismatch_capacity": `"concurrency":[{"group":"capacity","capacity":4}],`,
		"mismatch_scope":    `"concurrency":[{"group":"capacity","capacity":3,"scope":"run"}],`,
		"mismatch_on_limit": `"concurrency":[{"group":"capacity","capacity":3,"on_limit":"fail"}],`,
		"cost":              `"concurrency":[{"group":"capacity","capacity":3,"cost":2}],`,
		"cycle":             `"deps":["consumer"],`,
	}
	if field, ok := fields[mode]; ok {
		if closure == "second" {
			return json.RawMessage(`[]`)
		}
		return json.RawMessage(`[{` + field + `"id":"child","groups":["first"],"steps":[{"id":"body"}]}]`)
	}
	if mode != "fan256" && mode != "fan257" {
		return nil
	}
	jobs := []nodeproto.JobSpec{}
	if closure == "first" {
		count := 256
		if mode == "fan257" {
			count++
		}
		for i := range count {
			jobs = append(jobs, nodeproto.JobSpec{ID: fmt.Sprintf("child-%d", i), Groups: []string{"first"}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
		}
	}
	raw, _ := json.Marshal(jobs)
	return raw
}

func TestOwnerExpansion(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "empty", "failure", "refused", "unknown", "duplicate", "null", "concurrency", "mismatch_capacity", "mismatch_scope", "mismatch_on_limit", "cost", "cycle", "fan256", "fan257"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			owner := runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, Name: "trigger", EvalFence: runspec.EvalFence{RunID: "run", Holder: "owner", Generation: 9}}
			job := nodeproto.JobSpec{ID: "source", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"first", "second"}}}
			job.Concurrency = []nodeproto.ConcurrencySpec{{Group: "capacity", Capacity: new(3)}}
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
			if mode == "cycle" {
				plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: "consumer", Steps: []nodeproto.StepSpec{{ID: "body"}}, NeedsGenerated: []string{"first"}})
			}
			planHash, _ := runspec.CanonicalHash(plan)
			jobHash, _ := runspec.CanonicalHash(job)
			identity := engineid.Identity{Version: "v1.0.0", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.0.0"}}
			resolved := engine.Resolved{Identity: identity}
			request := runspec.SealedRequest{RunID: "run", Hash: "request", Request: runspec.Request{Pipeline: "pipeline"}}
			spec := runspec.Spec{Request: request, Hash: "spec", PlanHash: planHash, Resolved: runspec.Resolved{SecretRegistration: runspec.SecretRegistration{Version: 1}, Engine: identity}}
			rawPlan, _ := json.Marshal(plan)
			rawJob, _ := json.Marshal(job)
			s := newExpansionHostStore(store.RunRow{Execution: runstate.Execution{ID: "run", State: runstate.RunRunning, Owner: &owner, Request: request, Spec: &spec, Plan: rawPlan, PlanHash: string(planHash)}}, runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: "source"}, Status: runstate.StatusDone, Outcome: runstate.NodeSuccess, Generation: 4, Expansion: runstate.ExpansionExpected, Spec: rawJob, SpecHash: string(jobHash)})
			var mu sync.Mutex
			changed := make(chan struct{}, 1)
			readSnapshot, acceptExpansion := s.readSnapshot, s.acceptExpansion
			s.readSnapshot = func(ctx context.Context, run string) (store.RunSnapshot, error) {
				mu.Lock()
				defer mu.Unlock()
				return readSnapshot(ctx, run)
			}
			s.acceptExpansion = func(ctx context.Context, in store.ExpansionSubmission) (store.AcceptedExpansion, error) {
				mu.Lock()
				defer mu.Unlock()
				result, err := acceptExpansion(ctx, in)
				if err == nil {
					changed <- struct{}{}
				}
				return result, err
			}
			if mode == "cycle" {
				raw, err := json.Marshal(plan.Jobs[1])
				if err != nil {
					t.Fatal(err)
				}
				s.nodes = []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "consumer"}, Status: runstate.StatusPending, Spec: raw}}
			}
			refused := errors.New("expansion transaction refused")
			if mode == "refused" {
				s.reject = refused
			}
			process := newExpansionSDK(plan, mode)
			evaluator, err := newOwnerEvaluator(evaluationDependencies{Store: s, Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) { return process, nil }, EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				return resolved, nil
			}, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}}, ReadOutput: func(_ context.Context, ref runspec.NodeRef, generation int64) (json.RawMessage, error) {
				if ref != s.node.Ref || generation != 4 {
					t.Fatal("output read lost source fence")
				}
				return json.RawMessage(`{"value":7}`), nil
			}, Masker: testMasker("private")})
			if err != nil {
				t.Fatal(err)
			}
			entry := &source.Entry{Engine: resolved}
			runner := nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}
			host := OwnerHost{Masker: secrets.NewMasker(), Store: s, Renew: s.RenewOwner, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, Acquire: func(context.Context, RunInput) (*source.Entry, error) { return entry, nil }, Wait: func(ctx context.Context, _ store.RunSnapshot) error {
				select {
				case <-changed:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}, Expand: func(ctx context.Context, input EvaluationInput, row runstate.NodeRow, binding StorageBinding) (json.RawMessage, runspec.SecretRegistration, *store.ExpansionFailure, error) {
				if s.finished.Transition.RunID != "" || s.count != 0 {
					t.Fatal("owner settled before expansion")
				}
				if input.Entry != entry || !reflect.DeepEqual(input.Engine, resolved) || !reflect.DeepEqual(input.GrantedRunner, runner) || input.Accepted.SpecHash != string(spec.Hash) || runspec.NodeID(input.NodeID) != row.Ref.NodeID || runspec.ClaimGeneration(input.Generation) != row.Generation || input.SpecHash != row.SpecHash || runspec.RunID(input.RouteBinding.RunID) != row.Ref.RunID || runspec.NodeID(input.RouteBinding.JobID) != row.Ref.NodeID {
					t.Fatal("expansion lost acquired code or persisted evaluation identity")
				}
				return evaluator.evaluateExpansion(ctx, input)
			}}
			host.Delegator = ownerDelegatorFixture{actor: runstate.Actor{Kind: runstate.ActorRunHost, Owner: owner}}
			sealed, _ := json.Marshal(request.Request)
			result, err := host.Run(ctx, RunInput{RunID: "run", SealedRequest: sealed, RequestHash: "request", GrantedRunner: runner, Owner: runstate.Actor{Kind: runstate.ActorRunHost, Owner: owner}})
			if mode == "refused" {
				if !errors.Is(err, refused) || s.finished.Transition.RunID != "" {
					t.Fatalf("refused expansion finished: %+v, %v", s.finished, err)
				}
				return
			}
			if err != nil || s.count != 1 || s.submission.Source != s.node.Ref || s.submission.SourceGeneration != 4 || s.submission.Actor.Owner != owner {
				t.Fatalf("submission = %+v, count = %d, error = %v", s.submission, s.count, err)
			}
			if !process.cleaned || len(process.calls) != 2 {
				t.Fatalf("unjoined or incomplete evaluation: %+v", process)
			}
			mismatch := strings.TrimPrefix(mode, "mismatch_")
			if mode == "failure" || mode == "unknown" || mode == "duplicate" || mode == "null" || mode == "concurrency" || mode == "cycle" || mode == "fan257" || mismatch != mode {
				if result.Outcome != "failed" || s.submission.Failure == nil || len(s.submission.Body) != 0 || strings.Contains(s.submission.Failure.MaskedError, "private") {
					t.Fatalf("failure = %+v", s.submission)
				}
				wantCode := apierr.PlanInvalid
				if mode == "failure" {
					wantCode = apierr.SDKError
				}
				if s.submission.Failure.Code != wantCode || s.submission.Failure.MaskedError != "generator failed (details withheld: secret declarations unavailable)" {
					t.Fatalf("expansion failed for a different reason: %+v", s.submission.Failure)
				}
				return
			}
			var expanded nodeproto.PlanDoc
			if err := json.Unmarshal(s.submission.Body, &expanded); err != nil {
				t.Fatal(err)
			}
			want := 2
			if mode == "empty" {
				want = 0
			} else if mode == "fan256" {
				want = 256
			} else if mode == "cost" {
				want = 1
			}
			if result.Outcome != "success" || expanded.Jobs == nil || len(expanded.Jobs) != want || s.submission.Failure != nil {
				t.Fatalf("result = %+v, submission = %+v", result, s.submission)
			}
		})
	}
}
