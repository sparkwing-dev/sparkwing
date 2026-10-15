package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type expansionHostStore struct {
	ownerSealStore
	node       store.NodeRow
	submission store.ExpansionSubmission
	count      int
	reject     error
}

func (s *expansionHostStore) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{Run: s.row, Nodes: []store.NodeRow{s.node}}, nil
}

func (s *expansionHostStore) ReadNode(_ context.Context, ref store.NodeRef) (store.NodeRow, error) {
	if ref != s.node.Ref {
		return store.NodeRow{}, errors.New("unexpected source read")
	}
	return s.node, nil
}

func (s *expansionHostStore) AcceptExpansion(_ context.Context, in store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	s.count++
	if s.reject != nil {
		return store.AcceptedExpansion{}, s.reject
	}
	s.submission = in
	s.node.Expansion = store.ExpansionAccepted
	if in.Failure != nil {
		s.node.Expansion = store.ExpansionFailed
	}
	return store.AcceptedExpansion{Source: in.Source, SourceGeneration: in.SourceGeneration, State: s.node.Expansion}, nil
}

type expansionSDK struct {
	plan    nodeproto.PlanDoc
	mode    string
	calls   []string
	closed  bool
	cleaned bool
}

func (p *expansionSDK) Request(_ context.Context, in nodeproto.Request) (nodeproto.Reply, error) {
	var result any
	switch in.Op {
	case nodeproto.OpHello:
		result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.0.0"}}
	case nodeproto.OpPlan:
		result = p.plan
	case nodeproto.OpEval:
		p.calls = append(p.calls, in.Closure)
		if in.Job != "source" || string(in.Input) != `{"value":7}` {
			return nodeproto.Reply{}, errors.New("generator did not read recorded output")
		}
		if p.mode == "failure" && in.Closure == "second" {
			result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private failure"}}
			break
		}
		jobs := []nodeproto.JobSpec{}
		if p.mode != "empty" {
			jobs = append(jobs, nodeproto.JobSpec{ID: in.Closure, Groups: []string{in.Closure}})
		}
		raw, _ := json.Marshal(jobs)
		result = nodeproto.EvalResult{Value: raw}
	default:
		return nodeproto.Reply{}, fmt.Errorf("owner invoked %s", in.Op)
	}
	raw, err := json.Marshal(result)
	return nodeproto.Reply{ID: in.ID, OK: true, Result: raw}, err
}
func (p *expansionSDK) Close() error { p.closed = true; return nil }
func (p *expansionSDK) Cleanup(context.Context) error {
	if !p.closed {
		return errors.New("cleanup preceded process close")
	}
	p.cleaned = true
	return nil
}

func expansionHash(value any) (runspec.Hash, error) {
	raw, err := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return runspec.Hash(hex.EncodeToString(sum[:])), err
}

// CheckOwnerExpansion drives evaluation through the owner and its one expansion
// transaction. Store atomicity and native placement remain separate proofs.
func CheckOwnerExpansion(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "memoized", "empty", "failure", "refused"} {
		t.Run(mode, func(t *testing.T) {
			owner := store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Name: "trigger", Holder: "owner", Generation: 9}
			job := nodeproto.JobSpec{ID: "source", Closures: nodeproto.ClosureSpec{Generate: []string{"first", "second"}}}
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
			planHash, _ := expansionHash(plan)
			jobHash, _ := expansionHash(job)
			identity := engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
			resolved := engine.Resolved{Identity: identity}
			request := runspec.SealedRequest{RunID: "run", Hash: "request", Request: runspec.Request{Pipeline: "pipeline"}}
			spec := runspec.Spec{Request: request, Hash: "spec", PlanHash: planHash, Resolved: runspec.Resolved{Engine: identity}}
			rawPlan, _ := json.Marshal(plan)
			rawJob, _ := json.Marshal(job)
			s := &expansionHostStore{ownerSealStore: ownerSealStore{row: store.RunRow{ID: "run", State: store.RunRunning, Owner: &owner, Request: request, Spec: &spec, Plan: rawPlan, PlanHash: string(planHash)}}, node: store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "source"}, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: 4, Expansion: store.ExpansionExpected, Spec: rawJob, SpecHash: string(jobHash)}}
			if mode == "memoized" {
				s.node.Outcome = store.NodeMemoized
			}
			refused := errors.New("expansion transaction refused")
			if mode == "refused" {
				s.reject = refused
			}
			process := &expansionSDK{plan: plan, mode: mode}
			evaluator, err := nodehost.NewOwnerEvaluator(nodehost.EvaluationDependencies{Store: s, Start: func(context.Context, nodehost.EvaluationInput) (nodehost.ExecutorProcess, error) { return process, nil }, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return resolved, nil
			}, CanonicalHash: expansionHash, ValidatePlan: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
				var p nodeproto.PlanDoc
				err := json.Unmarshal(raw, &p)
				return p, err
			}, ReadOutput: func(_ context.Context, ref store.NodeRef, generation int64) (json.RawMessage, error) {
				if ref != s.node.Ref || generation != 4 {
					t.Fatal("output read lost source fence")
				}
				return json.RawMessage(`{"value":7}`), nil
			}, Mask: func(text string) string { return strings.ReplaceAll(text, "private", "***") }})
			if err != nil {
				t.Fatal(err)
			}
			entry := &source.Entry{Engine: resolved}
			host := nodehost.OwnerHost{Store: s, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, Acquire: func(context.Context, nodehost.RunInput) (*source.Entry, error) { return entry, nil }, Project: func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
				return store.ScheduleSnapshot{Nodes: []store.ScheduleNode{{ID: "source", Generation: 4, Expansion: string(snapshot.Nodes[0].Expansion)}}}, nil
			}, Decide: func(snapshot store.ScheduleSnapshot) store.ScheduleDecision {
				if snapshot.Nodes[0].Expansion == string(store.ExpansionExpected) {
					return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "source", Generation: 4, Kind: store.DecisionExpand}}}
				}
				outcome := "success"
				if snapshot.Nodes[0].Expansion == string(store.ExpansionFailed) {
					outcome = "failed"
				}
				return store.ScheduleDecision{Outcome: outcome}
			}, Wait: func(context.Context, store.RunSnapshot) error { return errors.New("expansion did not settle") }, Expand: func(ctx context.Context, accepted nodehost.AcceptedRun, row store.NodeRow) (json.RawMessage, *store.ExpansionFailure, error) {
				if s.finished.Transition.RunID != "" || s.count != 0 {
					t.Fatal("owner settled before expansion")
				}
				return evaluator.Expand(ctx, nodehost.EvaluationInput{Engine: resolved, Accepted: accepted, NodeID: row.Ref.NodeID, Generation: row.Generation, SpecHash: row.SpecHash, Entry: entry, RouteBinding: nodeproto.ProcessBinding{RunID: "run", JobID: "source"}})
			}}
			sealed, _ := json.Marshal(request.Request)
			result, err := host.Run(t.Context(), nodehost.RunInput{RunID: "run", SealedRequest: sealed, RequestHash: "request", Owner: store.Actor{Kind: store.ActorRunHost, Owner: owner}})
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
			if mode == "failure" {
				if result.Outcome != "failed" || s.submission.Failure == nil || len(s.submission.Body) != 0 || strings.Contains(s.submission.Failure.MaskedError, "private") {
					t.Fatalf("failure = %+v", s.submission)
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
			}
			if result.Outcome != "success" || expanded.Jobs == nil || len(expanded.Jobs) != want || s.submission.Failure != nil {
				t.Fatalf("result = %+v, submission = %+v", result, s.submission)
			}
		})
	}
}
