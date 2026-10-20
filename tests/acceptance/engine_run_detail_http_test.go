package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestEngineRunDetailRequiresStatusFacts(t *testing.T) {
	b := jobViewBoundary("member")
	f := snapshotFixture(b)
	e := fakeEngine(t, &f.engineTransactions)
	_, err := e.RunDetail(context.Background(), capability.Authorized{Target: capability.Target{RunID: "run", Team: "team"}})
	if err == nil {
		t.Fatal("missing consistent status facts must refuse the read")
	}
}

type runStatusFixture struct {
	facts             controller.RunStatusFacts
	reads             int
	err, ownershipErr error
	readerMissing     bool
}

func runStatusFacts(b *credentialBoundary) controller.RunStatusFacts {
	request := runspec.SealedRequest{
		RunID: "run", Hash: "private-first-seal", AuthorityRef: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "private-authority"},
		Request: runspec.Request{Team: "team", Pipeline: "build", RequestedArgs: map[string]string{"token": "private-secret", "count": "42"}},
	}
	identity := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 2, Generation: 4}
	return controller.RunStatusFacts{
		Snapshot: store.RunSnapshot{
			Run: store.RunRow{
				ID: "run", Team: "team", Number: 17, State: store.RunRunning, StateSeq: 9, Request: request,
				StartedAt: &b.snapshot.Now, CancelRequestedAt: &b.snapshot.Now,
				Spec: &runspec.Spec{Request: request, Hash: "private-second-seal", PlanHash: "private-plan-hash", Resolved: runspec.Resolved{
					Args: map[string]string{"token": "private-secret", "count": "42"}, SecretArgs: []string{"token"},
					Engine: engine.Identity{Version: "v1.2.3", ManifestDigest: "verified-manifest"},
				}},
				Plan: json.RawMessage(`{"env":{"TOKEN":"private-secret"}}`), StateDigest: "private-state-digest",
			},
			Nodes: []store.NodeRow{{
				Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusRunning, Attempt: 2, Generation: 4, StateSeq: 8,
				ClaimedBy: "private-holder", CapabilityPrefix: "private-prefix", SpecHash: "private-spec-hash",
				Spec: json.RawMessage(`{"id":"job","env":{"TOKEN":"private-secret"}}`),
			}},
			StoreNow: b.snapshot.Now,
		},
		Attempts: []store.NodeAttempt{
			{Identity: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 1, Generation: 3}, Holder: "private-old-holder", CapabilityPrefix: "private-old-prefix", ExecutorKind: "cloud", Outcome: store.NodeFailed, FailureReason: "disruption", StartedAt: b.snapshot.Now.Add(-time.Minute), FinishedAt: &b.snapshot.Now},
			{Identity: identity, Holder: "private-holder", CapabilityPrefix: "private-prefix", ExecutorKind: "customer", StartedAt: b.snapshot.Now},
		},
		Logs: []blob.LogSubject{
			{Kind: blob.LogJobAttempt, Team: "team", RunID: "run", Attempt: &identity},
			{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 7, Source: &blob.LogSource{NodeID: "job", Generation: 3}}},
		},
		Notices: []engine.Notice{{Kind: engine.NoticeSupportEnding, Reason: "support ends soon", Hint: "sparkwing upgrade"}},
	}
}

func runStatusEngine(t *testing.T, f *runStatusFixture) *controller.EngineController {
	t.Helper()
	e := &controller.EngineController{
		ResolveTenant: selectorTenant("team", f.ownershipErr),
	}
	if !f.readerMissing {
		e.ReadRunStatus = func(_ context.Context, team store.Team, run string) (controller.RunStatusFacts, error) {
			f.reads++
			if team != "team" || run != "run" {
				t.Fatalf("selected status team=%q run=%q", team, run)
			}
			return f.facts, f.err
		}
	}
	return e
}

func runStatusServer(t *testing.T, b *credentialBoundary, f *runStatusFixture) http.Handler {
	t.Helper()
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).
		WithEngineController(runStatusEngine(t, f)).Handler()
}

func TestEngineRunDetailHTTP(t *testing.T) {
	for _, identity := range []string{"member", "owner", "job", "session", "peer"} {
		t.Run(identity, func(t *testing.T) {
			b := jobViewBoundary(identity)
			if identity == "job" {
				b = newCredentialBoundary()
			}
			if identity == "session" || identity == "peer" {
				b = jobViewBoundary("member")
				p := &b.snapshot.Authorities[0].Principal
				p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
				if identity == "peer" {
					p.Class, p.Authority = capability.CredentialPeer, capability.AuthorityRef{}
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
				}
			}
			f := &runStatusFixture{facts: runStatusFacts(b)}
			h := runStatusServer(t, b, f)
			path := "/api/v1/runs/run?include=nodes,secret_values&team=foreign"
			request := snapshotRequest
			if identity == "session" || identity == "peer" {
				request = discoveryRequest
			}
			w := request(h, b, "GET", path)
			var got controllerwire.RunDetail
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || f.reads != 1 || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("HTTP %d %s reads=%d", w.Code, w.Body.String(), f.reads)
			}
			if got.ID != "run" || got.Team != "team" || got.Number != 17 || got.Status != "running" || got.StateSeq != 9 || len(got.Jobs) != 1 || got.Jobs[0].ID != "job" || got.Jobs[0].Attempt != 2 {
				t.Fatalf("status=%+v", got)
			}
			if len(got.Attempts) != 2 || got.Attempts[0].Attempt != 1 || got.Attempts[0].Generation != 3 || got.Attempts[0].Executor != "cloud" || got.Attempts[0].Reason != "disruption" || got.Attempts[1].Attempt != 2 || got.Attempts[1].Executor != "customer" || got.Attempts[1].FinishedAt != nil {
				t.Fatalf("attempts=%+v", got.Attempts)
			}
			if got.Inputs.Args["token"] != runspec.RedactedArgValue || got.Inputs.RequestedArgs["token"] != runspec.RedactedArgValue || got.Inputs.Args["count"] != "42" || !reflect.DeepEqual(got.Logs, f.facts.Logs) || !reflect.DeepEqual(got.Notices, f.facts.Notices) || got.Engine == nil || got.Engine.Version != "v1.2.3" {
				t.Fatalf("projection=%+v", got)
			}
			if w.Header().Get("Cache-Control") != "no-store" || b.bindingTarget.Team != "team" || b.bindingTarget.RunID != "run" || got.StartedAt == nil || got.CancelRequestedAt == nil {
				t.Fatalf("headers=%v target=%+v", w.Header(), b.bindingTarget)
			}
		})
	}
}

func TestEngineRunDetailEmptyBeforePlanning(t *testing.T) {
	b := jobViewBoundary("member")
	f := &runStatusFixture{facts: runStatusFacts(b)}
	f.facts.Snapshot.Run.Spec, f.facts.Snapshot.Nodes, f.facts.Attempts, f.facts.Logs, f.facts.Notices = nil, nil, nil, nil, nil
	w := snapshotRequest(runStatusServer(t, b, f), b, "GET", "/api/v1/runs/run")
	var got controllerwire.RunDetail
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || strings.Contains(w.Body.String(), "private-") || got.Engine != nil || got.Jobs == nil || got.Attempts == nil || got.Logs == nil || got.Notices == nil {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if got.Inputs.RequestedArgs["token"] != runspec.RedactedArgValue || got.Inputs.RequestedArgs["count"] != runspec.RedactedArgValue {
		t.Fatalf("unclassified inputs=%+v", got.Inputs)
	}
}

func TestEngineRunDetailClassification(t *testing.T) {
	for _, phase := range []string{"pending", "success"} {
		for _, classification := range []string{"absent", "intake", "resolved", "dropped intake", "unknown name", "duplicate name"} {
			t.Run(phase+"/"+classification, func(t *testing.T) {
				b := jobViewBoundary("member")
				f := &runStatusFixture{facts: runStatusFacts(b)}
				run := &f.facts.Snapshot.Run
				run.State = store.RunState(phase)
				wantPublic := false
				switch classification {
				case "absent":
					run.Spec = nil
				case "intake":
					run.Spec = nil
					run.Request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
					wantPublic = true
				case "resolved":
					wantPublic = true
				case "dropped intake":
					run.Request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
					run.Spec.Request = run.Request
					run.Spec.Resolved.SecretArgs = []string{}
				case "unknown name":
					run.Spec.Resolved.SecretArgs = []string{"missing"}
				case "duplicate name":
					run.Spec.Resolved.SecretArgs = []string{"token", "token"}
				}
				w := snapshotRequest(runStatusServer(t, b, f), b, "GET", "/api/v1/runs/run?include=secret_values")
				var got controllerwire.RunDetail
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || strings.Contains(w.Body.String(), "private-") || got.Inputs.RequestedArgs["token"] != runspec.RedactedArgValue {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
				if (got.Inputs.RequestedArgs["count"] == "42") != wantPublic {
					t.Fatalf("classification=%s public=%v inputs=%+v", classification, wantPublic, got.Inputs)
				}
			})
		}
	}
}

func TestEngineRunDetailRefusesUnrelatedAuthority(t *testing.T) {
	for _, scenario := range []string{"missing", "revoked", "expired", "membership removed", "missing parent", "finished claim", "finished run", "lease expired", "cancelled run", "machine", "publisher", "deployment", "wrong audience", "missing grant", "foreign team", "foreign run", "binding refusal", "private auth error", "missing unknown run"} {
		t.Run(scenario, func(t *testing.T) {
			b := newCredentialBoundary()
			p := &b.snapshot.Authorities[0].Principal
			f := &runStatusFixture{facts: runStatusFacts(b)}
			status, code := 401, apierr.Unauthenticated
			switch scenario {
			case "missing", "missing unknown run":
				b.raw = ""
				if scenario == "missing unknown run" {
					f.ownershipErr = store.ErrNotFound
				}
			case "revoked":
				p.Capability.RevokedAt = &b.snapshot.Now
			case "expired":
				p.Capability.ExpiresAt = &b.snapshot.Now
			case "membership removed":
				b.snapshot.Authorities[0].MembershipValid = false
			case "missing parent":
				p.Capability.Parent.ID = "absent"
			case "finished claim":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "finished run":
				b.snapshot.Binding.RunFinished = true
			case "lease expired":
				b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
			case "cancelled run":
				b.snapshot.Binding.RunCancelled = true
				status, code = 403, apierr.Forbidden
			case "machine", "deployment":
				b = jobViewBoundary("member")
				p = &b.snapshot.Authorities[0].Principal
				p.Capability.Subject.Kind = capability.SubjectMachine
				if scenario == "deployment" {
					p.Capability.Subject.Kind = capability.SubjectOperator
				}
				status, code = 403, apierr.Forbidden
			case "publisher":
				b = publisherBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
				status, code = 403, apierr.Forbidden
			case "wrong audience":
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				p.Capability.Audience, p.Capability.Digest = capability.NodeHost, sha256.Sum256([]byte(b.raw))
				b.snapshot.Binding.ProcessAlive = true
				status, code = 403, apierr.Forbidden
			case "missing grant":
				p.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "foreign team":
				p.Capability.Team = "foreign"
				status, code = 404, apierr.NotFound
			case "foreign run":
				p.Capability.Binding.RunID, b.snapshot.Binding.Current.RunID = "foreign", "foreign"
				status, code = 403, apierr.Forbidden
			case "binding refusal":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "not a member"}
				status, code = 403, apierr.Forbidden
			case "private auth error":
				b.readErr = errors.New("private-authentication")
				status, code = 500, apierr.Internal
			}
			w := snapshotRequest(runStatusServer(t, b, f), b, "GET", "/api/v1/runs/run")
			assertEngineError(t, w, status, code)
			if f.reads != 0 || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("refused read=%d body=%s", f.reads, w.Body.String())
			}
		})
	}
}

func TestEngineRunDetailErrorsAndMethods(t *testing.T) {
	for _, scenario := range []string{"missing reader", "not found", "unknown team", "no team", "private read error", "missing run", "private ownership error", "head", "post"} {
		t.Run(scenario, func(t *testing.T) {
			b := jobViewBoundary("member")
			f := &runStatusFixture{facts: runStatusFacts(b)}
			method, status, code := "GET", 500, apierr.Internal
			switch scenario {
			case "missing reader":
				f.readerMissing, code = true, apierr.PolicyMissing
			case "not found":
				f.err, status, code = store.ErrNotFound, 404, apierr.NotFound
			case "unknown team":
				f.err, status, code = store.ErrUnknownTeam, 404, apierr.NotFound
			case "no team":
				f.err, status, code = store.ErrNoTeam, 404, apierr.NotFound
			case "private read error":
				f.err = errors.New("private-status-database")
			case "missing run":
				f.ownershipErr, status, code = store.ErrNotFound, 404, apierr.NotFound
			case "private ownership error":
				f.ownershipErr = errors.New("private-run-lookup")
			case "head", "post":
				method, status, code = strings.ToUpper(scenario), 405, apierr.MethodNotAllowed
			}
			w := snapshotRequest(runStatusServer(t, b, f), b, method, "/api/v1/runs/run")
			assertEngineError(t, w, status, code)
			if strings.Contains(w.Body.String(), "private-") || ((f.readerMissing || f.ownershipErr != nil || method != "GET") && f.reads != 0) {
				t.Fatalf("reads=%d body=%s", f.reads, w.Body.String())
			}
		})
	}
}

// safety: private seal, declaration, claim and argument values never enter the status wire.
func FuzzEngineRunDetailPrivateProjection(f *testing.F) {
	f.Add([]byte("swk_secret\x00\n<&>"))
	f.Add([]byte("quote\"\\unicodeλ"))
	f.Fuzz(func(t *testing.T, value []byte) {
		digest := sha256.Sum256(value)
		private := "private-" + hex.EncodeToString(digest[:])
		for _, phase := range []string{"unclassified", "classified", "malformed"} {
			b := jobViewBoundary("member")
			facts := runStatusFacts(b)
			run := &facts.Snapshot.Run
			run.Request.Request.RequestedArgs["token"] = private
			run.Request.Request.Env.Digest, run.Request.Hash = runspec.Hash(private), runspec.Hash(private)
			run.Request.AuthorityRef.ID = private
			run.Plan, run.StateDigest = json.RawMessage(`{"secret":"`+private+`"}`), private
			run.Spec.Request, run.Spec.Hash = run.Request, runspec.Hash(private)
			run.Spec.Resolved.Args["token"] = private
			if phase == "unclassified" {
				run.Spec = nil
			} else if phase == "malformed" {
				run.Spec.Resolved.SecretArgs = nil
			}
			facts.Snapshot.Nodes[0].Spec = json.RawMessage(`{"secret":"` + private + `"}`)
			facts.Snapshot.Nodes[0].SpecHash, facts.Snapshot.Nodes[0].CapabilityPrefix, facts.Snapshot.Nodes[0].ClaimedBy = private, private, private
			facts.Attempts[0].CapabilityPrefix, facts.Attempts[0].Holder = private, private
			fixture := &runStatusFixture{facts: facts}
			w := snapshotRequest(runStatusServer(t, b, fixture), b, "GET", "/api/v1/runs/run?include=secret_values")
			if w.Code != 200 || strings.Contains(w.Body.String(), private) {
				t.Fatalf("%s: HTTP %d %s", phase, w.Code, w.Body.String())
			}
			var got controllerwire.RunDetail
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Inputs.RequestedArgs["token"] != runspec.RedactedArgValue {
				t.Fatalf("%s: secret was not redacted", phase)
			}
		}
	})
}

// safety: nested status facts must remain inside the resolved team, run and job set.
func FuzzEngineRunDetailScope(f *testing.F) {
	f.Add("foreign")
	f.Add("../team/run?include=secret_values")
	f.Fuzz(func(t *testing.T, selected string) {
		foreign := "foreign-" + selected
		for _, part := range []string{"team", "run", "request team", "request run", "spec team", "spec run", "spec seal", "job", "duplicate job", "attempt team", "attempt run", "attempt job", "attempt zero", "attempt future", "generation zero", "generation future", "duplicate attempt", "log team", "log run", "log attempt", "log generation", "log ordinal", "eval source", "clock"} {
			b := jobViewBoundary("member")
			facts := runStatusFacts(b)
			switch part {
			case "team":
				facts.Snapshot.Run.Team = store.Team(foreign)
			case "run":
				facts.Snapshot.Run.ID = foreign
			case "request team":
				facts.Snapshot.Run.Request.Request.Team = foreign
			case "request run":
				facts.Snapshot.Run.Request.RunID = foreign
			case "spec team":
				facts.Snapshot.Run.Spec.Request.Request.Team = foreign
			case "spec run":
				facts.Snapshot.Run.Spec.Request.RunID = foreign
			case "spec seal":
				facts.Snapshot.Run.Spec.Request.Hash = runspec.Hash(foreign)
			case "job":
				facts.Snapshot.Nodes[0].Ref.RunID = foreign
			case "duplicate job":
				facts.Snapshot.Nodes = append(facts.Snapshot.Nodes, facts.Snapshot.Nodes[0])
			case "attempt team":
				facts.Attempts[0].Identity.Team = foreign
			case "attempt run":
				facts.Attempts[0].Identity.RunID = foreign
			case "attempt job":
				facts.Attempts[0].Identity.NodeID = foreign
			case "attempt zero":
				facts.Attempts[0].Identity.Attempt = 0
			case "attempt future":
				facts.Attempts[0].Identity.Attempt = 3
			case "generation zero":
				facts.Attempts[0].Identity.Generation = 0
			case "generation future":
				facts.Attempts[0].Identity.Generation = 5
			case "duplicate attempt":
				facts.Attempts = append(facts.Attempts, facts.Attempts[0])
			case "log team":
				facts.Logs[0].Team = foreign
			case "log run":
				facts.Logs[0].RunID = foreign
			case "log attempt":
				facts.Logs[0].Attempt.RunID = foreign
			case "log generation":
				facts.Logs[0].Attempt.Generation = 99
			case "log ordinal":
				facts.Logs[0].Attempt.Attempt = 99
			case "eval source":
				facts.Logs[1].Unit.Source.NodeID = foreign
			case "clock":
				facts.Snapshot.StoreNow = time.Time{}
			}
			fixture := &runStatusFixture{facts: facts}
			w := snapshotRequest(runStatusServer(t, b, fixture), b, "GET", "/api/v1/runs/run")
			assertEngineError(t, w, 500, apierr.Internal)
			if strings.Contains(w.Body.String(), foreign) || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("%s: partial status escaped: %s", part, w.Body.String())
			}
		}
	})
}
