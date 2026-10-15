package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ownerBoundary() *credentialBoundary {
	b := planBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = capability.UnitTrigger, "trigger", capability.PurposeTrigger
	p.Capability.Binding.Kind = capability.BindingTrigger
	p.Capability.Grants = []capability.Grant{capability.RunsState, capability.TriggerHost, capability.NodeResult, capability.NodeReport}
	b.snapshot.Binding.Current = p.Capability.Binding
	return b
}

func publisherBoundary() *credentialBoundary {
	b := intakeBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
	p.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RunID: "run"}
	p.Capability.Grants = []capability.Grant{capability.DataWrite}
	b.snapshot.Binding.Current = p.Capability.Binding
	return b
}

func TestEngineLifecycleHTTP(t *testing.T) {
	for _, operation := range []string{"transition", "owner finish", "published finish", "cancel", "owner renewal", "job renewal", "retry"} {
		for _, name := range []string{"success", "revoked", "wrong grant", "bad body", "body actor", "trailing body", "store race", "private error"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				b := newCredentialBoundary()
				f := &engineTransactions{applied: store.Applied{Replayed: true, CancelRequested: true, StateSeq: 8, Generation: 4, Attempt: 2},
					beat:     store.LeaseBeat{ExpiresAt: b.snapshot.Now.Add(time.Minute), CancelRequested: true, CancelReason: "requested", Watched: true, WatchedNodes: []string{"job"}},
					admitted: store.AdmissionResult{RunID: "retry", Replayed: true}}
				path, body := "/api/v1/runs/run/jobs/job/transition", `{"to":"failed","cause":"report","result":{"outcome":"failed","reason":"exit","error":{"code":"sdk_error","message":"masked"},"exit_code":1}}`
				switch operation {
				case "owner finish":
					b, path, body = ownerBoundary(), "/api/v1/runs/run/finish", `{"outcome":"success"}`
				case "published finish":
					b, path, body = publisherBoundary(), "/api/v1/runs/run/finish", `{"outcome":"failed","reason":"exit","error":{"code":"sdk_error","message":"masked"}}`
				case "cancel":
					b, path, body = intakeBoundary(), "/api/v1/runs/run/cancel", `{"reason":"user requested"}`
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
				case "owner renewal":
					b, path, body = ownerBoundary(), "/api/v1/runs/run/heartbeat", `{"executing":true}`
				case "job renewal":
					path, body = "/api/v1/runs/run/jobs/job/heartbeat", `{"executing":true}`
				case "retry":
					b, path, body = intakeBoundary(), "/api/v1/runs/run/retry", `{"idempotency_key":"retry-once"}`
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
				}
				status, code, writes := http.StatusOK, apierr.Code(""), 1
				switch name {
				case "revoked":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
					status, code, writes = 401, apierr.Unauthenticated, 0
				case "wrong grant":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
					status, code, writes = 403, apierr.Forbidden, 0
				case "bad body":
					body = `null`
					status, code, writes = 400, apierr.BadRequest, 0
				case "body actor":
					body = body[:len(body)-1] + `,"actor":"forged"}`
					status, code, writes = 400, apierr.BadRequest, 0
				case "trailing body":
					body += `{}`
					status, code, writes = 400, apierr.BadRequest, 0
				case "store race":
					f.err = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}
					status, code = 409, apierr.LockHeld
				case "private error":
					f.err = errors.New("private credential")
					status, code = 500, apierr.Internal
				}
				w := engineHTTP(t, b, f, path, body)
				if f.writes != writes {
					t.Fatalf("writes = %d, want %d; HTTP %d %s", f.writes, writes, w.Code, w.Body.String())
				}
				if code != "" {
					assertEngineError(t, w, status, code)
					return
				}
				if w.Code != status {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
				p := b.snapshot.Authorities[0].Principal
				switch operation {
				case "transition":
					change := f.nodeChange
					if change.Ref != (store.NodeRef{RunID: "run", NodeID: "job"}) || change.Actor.Authority != p.Authority || change.Actor.Kind != store.ActorHolder || change.Actor.Generation != 4 || change.Actor.Holder != "runner" || change.Cause != store.CauseReport || change.To != store.NodeFailed || change.Extra.Result.MaskedError != "masked" {
						t.Fatalf("node transition = %+v", change)
					}
				case "owner finish":
					finish := f.finish
					if finish.Unit != finish.Transition.Actor.Owner || finish.Unit.Name != "trigger" || finish.Unit.Generation != 4 || finish.Transition.Actor.Authority != p.Authority || finish.Transition.Cause != store.RunFinish {
						t.Fatalf("finish = %+v", finish)
					}
				case "published finish":
					if f.runChange.Actor.Kind != store.ActorOperator || f.runChange.Cause != store.RunPublish || f.runChange.Actor.Owner != (store.OwnerLeaseRef{}) || f.runChange.MaskedError != "masked" {
						t.Fatalf("published finish = %+v", f.runChange)
					}
				case "cancel":
					if f.runChange.Actor.Authority != p.Authority || f.runChange.Actor.Kind != store.ActorOperator || f.runChange.Cause != store.RunCancel || f.runChange.To != store.RunCancelled || f.runChange.Reason != "user requested" {
						t.Fatalf("cancel = %+v", f.runChange)
					}
				case "owner renewal", "job renewal":
					if f.renewPrefix != p.Authority.ID || !f.renewExecuting {
						t.Fatalf("renewal actor = %+v", f.renewPrefix)
					}
					var response controllerwire.RenewalResponse
					if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ExpiresAt != f.beat.ExpiresAt || !response.CancelRequested || !response.Watched || len(response.WatchedJobs) != 1 {
						t.Fatalf("renewal = %s", w.Body.String())
					}
				case "retry":
					if f.intake.Authority != p.Authority || f.intake.IdempotencyKey != "retry-once" || f.intake.Request.Lineage.RetryOf != "run" || !f.intake.Request.Lineage.Retrier.Set || f.intake.Request.Lineage.Retrier.Value.ID != "account" {
						t.Fatalf("retry intake = %+v", f.intake)
					}
				}
				if operation == "transition" || operation == "owner finish" || operation == "published finish" || operation == "cancel" {
					var response controllerwire.Applied
					if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.StateSeq != 8 || response.Generation != 4 || response.Attempt != 2 || !response.Replayed || !response.CancelRequested {
						t.Fatalf("applied = %s", w.Body.String())
					}
				}
			})
		}
	}
}

func TestEngineLifecycleRefusesMalformedErrors(t *testing.T) {
	for _, failure := range []string{`{}`, `{"code":"sdk_error"}`, `{"message":"private"}`, `{"error":{"code":"sdk_error","message":"masked"}}`, `{"code":"sdk_error","code":"internal","message":"masked"}`} {
		b, f := ownerBoundary(), &engineTransactions{}
		w := engineHTTP(t, b, f, "/api/v1/runs/run/finish", `{"outcome":"failed","error":`+failure+`}`)
		assertEngineError(t, w, 400, apierr.BadRequest)
		if f.writes != 0 {
			t.Fatal("invalid error reached transaction")
		}
	}
}

func TestEngineResultReplayHTTP(t *testing.T) {
	for _, operation := range []string{"job", "run"} {
		for _, name := range []string{"exact", "changed body", "other route", "other actor", "other generation", "revoked ancestor"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				b := newCredentialBoundary()
				path, pattern, node := "/api/v1/runs/run/jobs/job/transition", "/api/v1/runs/{run}/jobs/{job}/transition", "job"
				body := `{"to":"success","cause":"report","result":{"outcome":"success"}}`
				if operation == "run" {
					b = ownerBoundary()
					path, pattern, node, body = "/api/v1/runs/run/finish", "/api/v1/runs/{run}/finish", "", `{"outcome":"success"}`
				}
				finished := b.snapshot.Now.Add(-time.Second)
				b.snapshot.Binding.FinishedAt = &finished
				digest := sha256.Sum256([]byte(body))
				b.snapshot.Binding.Result = &capability.RecordedResult{Actor: b.snapshot.Authorities[0].Principal.Authority,
					Method: "POST", Pattern: pattern, RunID: "run", NodeID: node, Generation: 4, Attempt: 2, Digest: hex.EncodeToString(digest[:])}
				switch name {
				case "changed body":
					body += " "
				case "other route":
					b.snapshot.Binding.Result.Pattern = "/api/v1/runs/{run}/plan"
				case "other actor":
					b.snapshot.Binding.Result.Actor.ID = "other"
				case "other generation":
					b.snapshot.Binding.Result.Generation++
				case "revoked ancestor":
					b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				}
				f := &engineTransactions{applied: store.Applied{Replayed: true}}
				w := engineHTTP(t, b, f, path, body)
				if name != "exact" {
					assertEngineError(t, w, 401, apierr.Unauthenticated)
					if f.writes != 0 {
						t.Fatal("unauthorized replay reached transaction")
					}
				} else {
					var response controllerwire.Applied
					if w.Code != 200 || f.writes != 1 || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Replayed {
						t.Fatalf("exact replay = %d %s", w.Code, w.Body.String())
					}
				}
			})
		}
	}
}

func TestEngineTypedTransitionRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason store.IllegalTransitionReason
		code   apierr.Code
	}{
		{store.ReasonHolderRights, apierr.LockHeld}, {store.ReasonStaleGeneration, apierr.LockHeld},
		{store.ReasonIllegalEdge, apierr.IllegalTransition}, {store.ReasonTerminal, apierr.IllegalTransition},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			f := &engineTransactions{err: &store.ErrIllegalTransition{Reason: tc.reason}}
			w := engineHTTP(t, newCredentialBoundary(), f, "/api/v1/runs/run/jobs/job/transition", `{"to":"success","cause":"report"}`)
			assertEngineError(t, w, 409, tc.code)
		})
	}
}

func TestEngineLifecycleMethodRefusal(t *testing.T) {
	handler := controller.New(nil, nil).Handler()
	for _, path := range []string{
		"/api/v1/runs/run/plan", "/api/v1/runs/run/finish", "/api/v1/runs/run/cancel", "/api/v1/runs/run/retry",
		"/api/v1/runs/run/heartbeat", "/api/v1/runs/run/jobs/job/heartbeat", "/api/v1/runs/run/jobs/job/transition",
	} {
		for _, method := range []string{"GET", "HEAD", "PUT", "OPTIONS"} {
			t.Run(method+path, func(t *testing.T) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
				assertEngineError(t, w, 405, apierr.MethodNotAllowed)
				if w.Header().Get("Allow") != "POST" {
					t.Fatalf("Allow = %q", w.Header().Get("Allow"))
				}
			})
		}
	}
}
