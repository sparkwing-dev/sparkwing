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
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type engineTransactions struct {
	intake    store.IntakeRequest
	plan      store.PlanAcceptance
	expansion store.ExpansionSubmission
	run       store.RunRow
	node      store.NodeRow
	admitted  store.AdmissionResult
	accepted  store.AcceptedPlan
	expanded  store.AcceptedExpansion
	writes    int
	err       error
	readErr   error
}

func (f *engineTransactions) AdmitRun(_ context.Context, request store.IntakeRequest) (store.AdmissionResult, error) {
	f.intake = request
	f.writes++
	return f.admitted, f.err
}

func (f *engineTransactions) ReadRun(_ context.Context, run string) (store.RunRow, error) {
	return f.run, f.readErr
}

func (f *engineTransactions) ReadNode(_ context.Context, ref store.NodeRef) (store.NodeRow, error) {
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
		ResolveTenant: func(_ context.Context, team store.Team) (controller.RunTransactions, error) {
			if team != "team" {
				t.Fatalf("resolved tenant %q", team)
			}
			return f, nil
		},
		ResolveRunTeam: func(context.Context, string) (store.Team, error) { return "team", nil },
	}
}

func TestEngineAdaptersCallFinalTransactions(t *testing.T) {
	a := expansionAuthority()
	f := &engineTransactions{
		admitted: store.AdmissionResult{RunID: "admitted", Number: 17, HereBearer: []byte("one-time-here")},
		run:      store.RunRow{ID: "run", Team: "team", Request: runspec.SealedRequest{RunID: "run", Hash: "persisted-first-seal"}},
		accepted: store.AcceptedPlan{PlanHash: "second-seal", Plan: json.RawMessage(`{"jobs":[]}`), Replayed: true},
		expanded: store.AcceptedExpansion{
			SourceGeneration: 4, State: store.ExpansionAccepted, BodyHash: "expansion-seal", Replayed: true,
			Nodes: []store.NodeRow{{Spec: json.RawMessage(`{"id":"generated"}`), SpecHash: "generated-seal"}},
		},
	}
	e := fakeEngine(t, f)
	created, err := e.CreateRun(t.Context(), a, controllerwire.CreateRunRequest{Request: runspec.Request{Pipeline: "build"}, IdempotencyKey: "once"})
	if err != nil || created.RunID != "admitted" || created.Number != 17 || created.HereBearer != "one-time-here" || created.Replayed ||
		f.intake.Authority != a.Principal.Authority || f.intake.IdempotencyKey != "once" {
		t.Fatalf("intake = %+v, %+v, %v", created, f.intake, err)
	}
	f.admitted.HereBearer, f.admitted.Replayed = nil, true
	replayed, err := e.CreateRun(t.Context(), a, controllerwire.CreateRunRequest{IdempotencyKey: "once"})
	if err != nil || !replayed.Replayed || replayed.HereBearer != "" {
		t.Fatalf("intake replay = %+v, %v", replayed, err)
	}
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
		!f.plan.Seal.Now.IsZero() || f.plan.Actor.Authority != a.Principal.Authority || f.plan.Actor.Kind != store.ActorHolder || !reflect.DeepEqual(body.Declarations, f.plan.Seal.Declarations) {
		t.Fatalf("plan = %+v, %+v, %v", planned, f.plan, err)
	}
	expanded, err := e.AcceptExpansion(t.Context(), a, controllerwire.ExpansionSubmission{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]}`)})
	if err != nil || !expanded.Replayed || expanded.BodyHash != "expansion-seal" || len(expanded.Jobs) != 1 || expanded.Jobs[0].Job.ID != "generated" || expanded.Jobs[0].SpecHash != "generated-seal" {
		t.Fatalf("expansion = %+v, %v", expanded, err)
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
	if err != nil || f.plan.Actor.Authority != a.Principal.Authority || f.plan.Actor.CapabilityPrefix != "" || f.plan.Actor.Owner != (store.OwnerLeaseRef{}) {
		t.Fatalf("publisher actor = %+v, %v", f.plan.Actor, err)
	}
	f.run.Team = "other"
	before := f.writes
	if _, err := fakeEngine(t, f).AcceptPlan(t.Context(), a, controllerwire.PlanSubmission{}); err == nil || f.writes != before {
		t.Fatal("foreign first seal reached plan acceptance")
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
				node:     store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Generation: 4},
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
				b.snapshot.Authorities = []capability.AuthorityState{b.snapshot.Authorities[0], {Principal: owner, MembershipValid: true}, b.snapshot.Authorities[1]}
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
