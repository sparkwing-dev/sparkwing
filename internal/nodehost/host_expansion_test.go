package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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

func expansionHash(value any) (runspec.Hash, error) {
	raw, err := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return runspec.Hash(hex.EncodeToString(sum[:])), err
}

func TestOwnerExpansion(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "empty", "failure", "refused", "unknown", "duplicate", "null", "concurrency", "mismatch_capacity", "mismatch_scope", "mismatch_on_limit", "cost", "cycle", "fan256", "fan257"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			owner := store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Name: "trigger", Holder: "owner", Generation: 9}
			job := nodeproto.JobSpec{ID: "source", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"first", "second"}}}
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "capacity", Capacity: new(3)}}}
			if mode == "cycle" {
				plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: "consumer", Steps: []nodeproto.StepSpec{{ID: "body"}}, NeedsGenerated: []string{"first"}})
			}
			planHash, _ := expansionHash(plan)
			jobHash, _ := expansionHash(job)
			identity := engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
			resolved := engine.Resolved{Identity: identity}
			request := runspec.SealedRequest{RunID: "run", Hash: "request", Request: runspec.Request{Pipeline: "pipeline"}}
			spec := runspec.Spec{Request: request, Hash: "spec", PlanHash: planHash, Resolved: runspec.Resolved{Engine: identity}}
			rawPlan, _ := json.Marshal(plan)
			rawJob, _ := json.Marshal(job)
			s := newExpansionHostStore(store.RunRow{ID: "run", State: store.RunRunning, Owner: &owner, Request: request, Spec: &spec, Plan: rawPlan, PlanHash: string(planHash)}, store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "source"}, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: 4, Expansion: store.ExpansionExpected, Spec: rawJob, SpecHash: string(jobHash)})
			if mode == "cycle" {
				raw, err := json.Marshal(plan.Jobs[1])
				if err != nil {
					t.Fatal(err)
				}
				s.nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "consumer"}, Status: store.StatusPending, Spec: raw}}
			}
			refused := errors.New("expansion transaction refused")
			if mode == "refused" {
				s.reject = refused
			}
			process := newExpansionSDK(plan, mode)
			evaluator, err := NewOwnerEvaluator(EvaluationDependencies{Store: s, Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) { return process, nil }, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return resolved, nil
			}, CanonicalHash: expansionHash, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}}, ReadOutput: func(_ context.Context, ref controllerwire.NodeRef, generation int64) (json.RawMessage, error) {
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
			host := OwnerHost{Masker: secrets.NewMasker(), Store: s, Renew: s.RenewOwner, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, Acquire: func(context.Context, RunInput) (*source.Entry, error) { return entry, nil }, Wait: func(context.Context, store.RunSnapshot) error { return errors.New("expansion did not settle") }, Expand: func(ctx context.Context, input EvaluationInput, row store.NodeRow, binding StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
				if s.finished.Transition.RunID != "" || s.count != 0 {
					t.Fatal("owner settled before expansion")
				}
				if input.Entry != entry || !reflect.DeepEqual(input.Engine, resolved) || !reflect.DeepEqual(input.GrantedRunner, runner) || input.Accepted.SpecHash != string(spec.Hash) || input.NodeID != row.Ref.NodeID || input.Generation != row.Generation || input.SpecHash != row.SpecHash || input.RouteBinding.RunID != row.Ref.RunID || input.RouteBinding.JobID != row.Ref.NodeID {
					t.Fatal("expansion lost acquired code or persisted evaluation identity")
				}
				return evaluator.Expand(ctx, input)
			}}
			host.Delegator = ownerDelegatorFixture{actor: store.Actor{Kind: store.ActorRunHost, Owner: owner}}
			sealed, _ := json.Marshal(request.Request)
			result, err := host.Run(ctx, RunInput{RunID: "run", SealedRequest: sealed, RequestHash: "request", GrantedRunner: runner, Owner: store.Actor{Kind: store.ActorRunHost, Owner: owner}})
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
				reason := map[string]string{
					"unknown": "unknown field", "duplicate": "duplicate field", "null": "null is not a declaration",
					"concurrency": "explicit capacity and cost must be positive", "cycle": "dependency cycle",
					"fan257": "generator fan-out exceeds 256 jobs",
				}[mode]
				if reason != "" && (s.submission.Failure.Code != apierr.PlanInvalid || !strings.Contains(s.submission.Failure.MaskedError, reason)) {
					t.Fatalf("expansion failed for a different reason: %+v", s.submission.Failure)
				}
				if mismatch != mode && !strings.Contains(s.submission.Failure.MaskedError, "jobs[0].concurrency[0]."+mismatch) {
					t.Fatalf("mismatch lost its field path: %+v", s.submission.Failure)
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
