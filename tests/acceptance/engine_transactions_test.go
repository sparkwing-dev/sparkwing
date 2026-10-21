package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type engineTransactions struct {
	intake            store.IntakeRequest
	plan              store.PlanAcceptance
	expansion         store.ExpansionSubmission
	run               store.RunRow
	node              store.NodeRow
	admitted          store.AdmissionResult
	accepted          store.AcceptedPlan
	expanded          store.AcceptedExpansion
	nodeChange        store.NodeCommand
	runChange         store.RunTransition
	finish            store.FinishRunRequest
	renewPrefix       string
	renewExecuting    bool
	beat              store.LeaseBeat
	applied           store.Applied
	annotation        store.AnnotationWrite
	summary           store.SummaryWrite
	step              store.StepReport
	authorityActor    store.Actor
	revokedAuthority  capability.AuthorityRef
	membership        store.MembershipUpdate
	members           []store.Membership
	memberReads       int
	commitAuthority   func()
	writes            int
	err               error
	readErr           error
	runReadErr        error
	failure           *nodeproto.JobFailure
	failureRef        controllerwire.NodeRef
	failureGeneration int64
	failureActor      store.Actor
	failureReads      int
}

func (f *engineTransactions) ReconcileNode(context.Context, store.ReconcileRequest) (store.ReconciledNode, error) {
	return store.ReconciledNode{}, f.err
}

func (f *engineTransactions) ReadFailure(_ context.Context, source controllerwire.NodeRef, generation int64, actor store.Actor) (*nodeproto.JobFailure, error) {
	f.failureRef, f.failureGeneration, f.failureActor = source, generation, actor
	f.failureReads++
	return f.failure, f.readErr
}

func (f *engineTransactions) Members(context.Context) ([]store.Membership, error) {
	f.memberReads++
	return f.members, f.readErr
}

func (f *engineTransactions) RevokeAuthority(_ context.Context, actor store.Actor, target capability.AuthorityRef) error {
	f.authorityActor, f.revokedAuthority = actor, target
	f.writes++
	if f.err == nil && f.commitAuthority != nil {
		f.commitAuthority()
	}
	return f.err
}

func (f *engineTransactions) UpdateMembership(_ context.Context, actor store.Actor, change store.MembershipUpdate) error {
	f.authorityActor, f.membership = actor, change
	f.writes++
	if f.err == nil && f.commitAuthority != nil {
		f.commitAuthority()
	}
	return f.err
}

func (f *engineTransactions) WriteSummary(_ context.Context, summary store.SummaryWrite) error {
	f.summary = summary
	f.writes++
	return f.err
}

func (f *engineTransactions) RecordStep(_ context.Context, step store.StepReport) error {
	f.step = step
	f.writes++
	return f.err
}

func (f *engineTransactions) WriteAnnotation(_ context.Context, annotation store.AnnotationWrite) error {
	f.annotation = annotation
	f.writes++
	return f.err
}

func (f *engineTransactions) TransitionNode(_ context.Context, change store.NodeCommand) (store.Applied, error) {
	f.nodeChange = change
	f.writes++
	return f.applied, f.err
}

func (f *engineTransactions) TransitionRun(_ context.Context, change store.RunTransition) (store.Applied, error) {
	f.runChange = change
	f.writes++
	return f.applied, f.err
}

func (f *engineTransactions) FinishRunAndUnit(_ context.Context, finish store.FinishRunRequest) (store.Applied, error) {
	f.finish = finish
	f.writes++
	return f.applied, f.err
}

func (f *engineTransactions) RenewUnit(_ context.Context, prefix string, executing bool) (store.LeaseBeat, error) {
	f.renewPrefix, f.renewExecuting = prefix, executing
	f.writes++
	return f.beat, f.err
}

func (f *engineTransactions) ReadOwnerSnapshot(ctx context.Context, _ capability.AuthorityRef, run string) (store.RunSnapshot, error) {
	return f.ReadSnapshot(ctx, run)
}

func (f *engineTransactions) ReplayRun(context.Context, store.IntakeRequest) (store.AdmissionResult, bool, error) {
	return store.AdmissionResult{}, false, nil
}

func (f *engineTransactions) AdmitRun(_ context.Context, request store.IntakeRequest) (store.AdmissionResult, error) {
	f.intake = request
	f.writes++
	return f.admitted, f.err
}

func (f *engineTransactions) ReadRun(_ context.Context, run string) (store.RunRow, error) {
	return f.run, f.runReadErr
}

func (f *engineTransactions) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{}, f.readErr
}

func (f *engineTransactions) ReadNode(_ context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
	if ref != f.node.Ref {
		return store.NodeRow{}, &apierr.Error{Code: apierr.NotFound, Message: "job not found"}
	}
	return f.node, f.readErr
}

func (f *engineTransactions) AcceptPlan(_ context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
	f.plan = request
	f.writes++
	return f.accepted, f.err
}

func (f *engineTransactions) AcceptExpansion(_ context.Context, request store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	f.expansion = request
	f.writes++
	return f.expanded, f.err
}

func fakeEngine(t *testing.T, f *engineTransactions) *controller.EngineController {
	t.Helper()
	return &controller.EngineController{
		Units: f,
		ResolveTenant: func(_ context.Context, team store.Team) (controller.TenantHandle, error) {
			if team != "team" {
				return nil, store.ErrNotFound
			}
			return f, nil
		},
	}
}

func TestEngineAdaptersCallFinalTransactions(t *testing.T) {
	a := expansionAuthority()
	f := &engineTransactions{
		run:      store.RunRow{ID: "run", Team: "team", Request: runspec.SealedRequest{RunID: "run", Hash: "persisted-first-seal"}},
		accepted: store.AcceptedPlan{PlanHash: "second-seal", Plan: json.RawMessage(`{"jobs":[]}`), Replayed: true},
		expanded: store.AcceptedExpansion{
			SourceGeneration: 4, State: store.ExpansionAccepted, BodyHash: "expansion-seal", Replayed: true,
			Nodes: []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "generated"}, Spec: json.RawMessage(`{"id":"generated"}`), SpecHash: "generated-seal", Status: store.StatusRunning, Generation: 8, Attempt: 3, StateSeq: 12, ClaimedBy: "current-holder", ApplicationRetries: 2, Disruptions: 1}},
		},
	}
	e := fakeEngine(t, f)
	body := controllerwire.PlanSubmission{Plan: json.RawMessage(`{"jobs":[]}`), Declarations: runspec.CheckoutDeclarations{
		Version: 1, File: "sparkwing.yaml",
		Pipeline: runspec.CheckoutLayer{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"linux"}}}},
		Defaults: runspec.CheckoutLayer{Prefers: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"arm64"}}}},
	}}
	planner := a
	planner.Principal.Capability.Unit = capability.UnitPlan
	planner.Binding.Kind, planner.Binding.NodeID = capability.BindingRun, ""
	planned, err := e.AcceptPlan(t.Context(), planner, body)
	if err != nil || planned.PlanHash != "second-seal" || !planned.Replayed || f.plan.Seal.Request.Hash != "persisted-first-seal" ||
		!f.plan.Seal.Now.IsZero() || f.plan.Actor.Authority != a.Principal.Authority || f.plan.Actor.Kind != store.ActorRunHost || !reflect.DeepEqual(body.Declarations, f.plan.Seal.Declarations) {
		t.Fatalf("plan = %+v, %+v, %v", planned, f.plan, err)
	}
	expanded, err := e.AcceptExpansion(t.Context(), a, controllerwire.ExpansionSubmission{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]}`)})
	if err != nil || !expanded.Replayed || expanded.BodyHash != "expansion-seal" || len(expanded.Jobs) != 1 || expanded.Jobs[0].Job.ID != "generated" || expanded.Jobs[0].SpecHash != "generated-seal" {
		t.Fatalf("expansion = %+v, %v", expanded, err)
	}
	current := expanded.Jobs[0].Current
	if current.ID != "generated" || current.Status != "running" || current.Generation != 8 || current.Attempt != 3 || current.StateSeq != 12 || current.ClaimedBy != "current-holder" || current.ApplicationRetries != 2 || current.Disruptions != 1 || current.SpecHash != "generated-seal" {
		t.Fatalf("replayed expansion lost current execution state: %+v", current)
	}
	refusal := &apierr.Error{Code: apierr.LockHeld, Message: "transaction fence changed"}
	f.err = refusal
	if _, err := e.AcceptExpansion(t.Context(), a, controllerwire.ExpansionSubmission{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]}`)}); !errors.Is(err, refusal) {
		t.Fatalf("transaction refusal = %v", err)
	}
}

func TestEnginePlanPublisherKeepsSessionAuthority(t *testing.T) {
	a := expansionAuthority()
	a.Principal.Class = capability.CredentialSession
	a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session"}
	a.Principal.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "account"}
	a.Principal.Capability.Unit, a.Principal.Capability.UnitID, a.Principal.Capability.Purpose = "", "", ""
	a.Principal.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RunID: "run"}
	a.Principal.Capability.Grants = []capability.Grant{capability.DataWrite}
	a.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
	a.Principal.Capability.Binding = a.Binding
	a.Access = capability.AccessRule{Credential: capability.CredentialPublisher, Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveCredential}
	f := &engineTransactions{run: store.RunRow{ID: "run", Team: "team", Request: runspec.SealedRequest{RunID: "run"}}}
	_, err := fakeEngine(t, f).AcceptPlan(t.Context(), a, controllerwire.PlanSubmission{Plan: json.RawMessage(`{"jobs":[]}`)})
	if err != nil || f.plan.Actor.Kind != store.ActorOperator || f.plan.Actor.Authority != a.Principal.Authority || f.plan.Actor.CapabilityPrefix != "" || f.plan.Actor.Owner != (store.OwnerLeaseRef{}) {
		t.Fatalf("publisher actor = %+v, %v", f.plan.Actor, err)
	}
	f.run.Team = "other"
	before := f.writes
	if _, err := fakeEngine(t, f).AcceptPlan(t.Context(), a, controllerwire.PlanSubmission{}); err == nil || f.writes != before {
		t.Fatal("foreign first seal reached plan acceptance")
	}
}

func TestEngineRetryPlanKeepsSourceSeal(t *testing.T) {
	a := expansionAuthority()
	a.Principal.Capability.Unit = capability.UnitPlan
	a.Target.RunID = "retry"
	a.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "retry", Holder: "runner", Generation: 4}
	f := &engineTransactions{run: store.RunRow{ID: "retry", Team: "team", Request: runspec.SealedRequest{RunID: "source", Hash: "original-seal"}}}
	if _, err := fakeEngine(t, f).AcceptPlan(t.Context(), a, controllerwire.PlanSubmission{Plan: json.RawMessage(`{"jobs":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if f.plan.RunID != "retry" || !reflect.DeepEqual(f.plan.Seal.Request, f.run.Request) {
		t.Fatalf("retry plan changed its first seal: %+v", f.plan)
	}
}

func expansionServer(t *testing.T, b *credentialBoundary, f *engineTransactions) http.Handler {
	t.Helper()
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithAuthorization(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(b), Bindings: b}).
		WithEngineController(fakeEngine(t, f)).Handler()
}

func TestEngineExpansionHTTP(t *testing.T) {
	for _, name := range []string{"success", "failure", "replay", "owner", "anonymous", "ended source claim", "revoked", "cancelled", "expired eval", "expired controller", "wrong grant", "wrong audience", "sibling source", "wrong run", "stale source", "stale persisted generation", "transaction race", "bad body", "body actor", "trailing body", "nested error", "missing error code", "duplicate error code", "unknown source", "dead with unknown source", "private store error", "corrupt persisted expansion"} {
		t.Run(name, func(t *testing.T) {
			b := newCredentialBoundary()
			a := expansionAuthority()
			b.snapshot.Authorities[0].Principal = a.Principal
			finished := b.snapshot.Now.Add(-time.Second)
			leaseUntil := b.snapshot.Now.Add(time.Minute)
			b.snapshot.Binding.FinishedAt = &finished
			b.snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *a.Binding.Expansion, ExpiresAt: leaseUntil, ControllerLeaseExpiresAt: &leaseUntil}
			f := &engineTransactions{
				node:     store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Generation: 4},
				expanded: store.AcceptedExpansion{SourceGeneration: 4, State: store.ExpansionAccepted, BodyHash: "committed"},
			}
			path := "/api/v1/runs/run/jobs/job/expansion"
			body := `{"source_generation":4,"body":{"jobs":[]}}`
			status, code := http.StatusOK, apierr.Code("")
			switch name {
			case "failure":
				body = `{"source_generation":4,"failure":{"code":"unavailable","message":"generator failed"}}`
				f.expanded.State = store.ExpansionFailed
			case "replay":
				f.expanded.Replayed = true
			case "owner":
				owner := a.Principal
				owner.Authority.ID, owner.Capability.Prefix = "run-owner", "run-owner"
				owner.Capability.Unit, owner.Capability.UnitID, owner.Capability.Purpose = capability.UnitTrigger, "trigger", capability.PurposeTrigger
				owner.Capability.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Generation: 9, Holder: "controller"}
				child := &b.snapshot.Authorities[0].Principal
				child.Capability.Unit, child.Capability.UnitID, child.Capability.Purpose = owner.Capability.Unit, owner.Capability.UnitID, owner.Capability.Purpose
				child.Capability.Issuance, child.Capability.Parent = capability.Delegate, &owner.Authority
				child.Capability.Binding.Expansion.Kind, child.Capability.Binding.Expansion.ID = capability.UnitTrigger, "trigger"
				if err := capability.ValidateDelegation(owner, capability.DelegateSpec{Parent: owner.Authority, Audience: capability.Controller, Grants: child.Capability.Grants, Binding: child.Capability.Binding, ExpiresAt: child.Capability.ExpiresAt}, b.snapshot.Now); err != nil {
					t.Fatalf("owner expansion is not issuable: %v", err)
				}
				b.snapshot.Binding.Expansion.Lease = *child.Capability.Binding.Expansion
				b.snapshot.Authorities = []capability.AuthorityState{b.snapshot.Authorities[0], {Principal: owner}, b.snapshot.Authorities[1]}
			case "anonymous":
				b.raw = ""
				status, code = 401, apierr.Unauthenticated
			case "ended source claim":
				b.snapshot.Authorities[0].Principal.Capability.Binding.Expansion = nil
				b.snapshot.Binding.Expansion = nil
				status, code = 401, apierr.Unauthenticated
			case "revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				status, code = 401, apierr.Unauthenticated
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
				status, code = 401, apierr.Unauthenticated
			case "expired eval":
				b.snapshot.Binding.Expansion.ExpiresAt = b.snapshot.Now
				status, code = 401, apierr.Unauthenticated
			case "expired controller":
				b.snapshot.Binding.Expansion.ControllerLeaseExpiresAt = &b.snapshot.Now
				status, code = 401, apierr.Unauthenticated
			case "wrong grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "wrong audience":
				b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
				b.snapshot.Binding.ProcessAlive = true
				status, code = 403, apierr.Forbidden
			case "sibling source":
				path = "/api/v1/runs/run/jobs/sibling/expansion"
				f.node.Ref.NodeID = "sibling"
				status, code = 403, apierr.Forbidden
			case "wrong run":
				path = "/api/v1/runs/other/jobs/job/expansion"
				f.node.Ref.RunID = "other"
				status, code = 403, apierr.Forbidden
			case "stale source":
				body = `{"source_generation":3,"body":{"jobs":[]}}`
				status, code = 409, apierr.LockHeld
			case "stale persisted generation":
				f.node.Generation = 5
				body = `{"source_generation":5,"body":{"jobs":[]}}`
				status, code = 409, apierr.LockHeld
			case "transaction race":
				f.err = &apierr.Error{Code: apierr.LockHeld, Message: "transaction fence changed"}
				status, code = 409, apierr.LockHeld
			case "bad body":
				body = `{"source_generation":4,"body":{"jobs":null}}`
				status, code = 400, apierr.BadRequest
			case "body actor":
				body = `{"source_generation":4,"body":{"jobs":[]},"actor":"forged"}`
				status, code = 400, apierr.BadRequest
			case "trailing body":
				body += `{}`
				status, code = 400, apierr.BadRequest
			case "nested error":
				body = `{"source_generation":4,"failure":{"error":{"code":"unavailable","message":"failed"}}}`
				status, code = 400, apierr.BadRequest
			case "missing error code":
				body = `{"source_generation":4,"failure":{"message":"failed"}}`
				status, code = 400, apierr.BadRequest
			case "duplicate error code":
				body = `{"source_generation":4,"failure":{"code":"unavailable","code":"internal","message":"failed"}}`
				status, code = 400, apierr.BadRequest
			case "unknown source":
				f.readErr = &apierr.Error{Code: apierr.NotFound, Message: "job not found"}
				status, code = 404, apierr.NotFound
			case "dead with unknown source":
				f.readErr = &apierr.Error{Code: apierr.NotFound, Message: "job not found"}
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				status, code = 401, apierr.Unauthenticated
			case "private store error":
				f.err = errors.New("private database credential")
				status, code = 500, apierr.Internal
			case "corrupt persisted expansion":
				f.expanded.Nodes = []store.NodeRow{{Spec: json.RawMessage(`null`)}}
				status, code = 500, apierr.Internal
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+b.raw)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Sparkwing-Generation", "999")
			r.Header.Set("X-Sparkwing-Holder", "forged")
			w := httptest.NewRecorder()
			expansionServer(t, b, f).ServeHTTP(w, r)
			if w.Code != status || strings.Contains(w.Body.String(), "private database credential") {
				t.Fatalf("HTTP %d %s, want %d", w.Code, w.Body.String(), status)
			}
			if code != "" {
				wantWrites := 0
				if name == "transaction race" || name == "private store error" || name == "corrupt persisted expansion" {
					wantWrites = 1
				}
				if f.writes != wantWrites {
					t.Fatalf("refused request reached %d writes, want %d", f.writes, wantWrites)
				}
				var envelope apierr.Envelope
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != code {
					t.Fatalf("error = %s, %v", w.Body.String(), err)
				}
				return
			}
			if f.writes != 1 || f.expansion.Actor.Generation != 9 || f.expansion.SourceGeneration != 4 ||
				f.expansion.Actor.Authority != a.Principal.Authority || f.expansion.Actor.Holder != "controller" {
				t.Fatalf("transaction request = %+v, writes %d", f.expansion, f.writes)
			}
			var response controllerwire.ExpansionAccepted
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Jobs == nil || response.Replayed != f.expanded.Replayed || response.State != string(f.expanded.State) {
				t.Fatalf("response = %s, %v", w.Body.String(), err)
			}
		})
	}
}

var _ controller.RunTransactions = (*engineTransactions)(nil)

func TestEngineExpansionHTTPMethodRefusal(t *testing.T) {
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, method := range []string{"GET", "HEAD", "PUT", "OPTIONS"} {
		r := httptest.NewRequest(method, "/api/v1/runs/run/jobs/job/expansion", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: %d %s, Allow %q", method, w.Code, w.Body.String(), w.Header().Get("Allow"))
		}
		var envelope apierr.Envelope
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != apierr.MethodNotAllowed {
			t.Fatalf("%s: error %s, %v", method, w.Body.String(), err)
		}
	}
}

func (f *engineTransactions) Team() store.Team { return "team" }
