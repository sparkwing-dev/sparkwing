package acceptance_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestEngineJobCancelHTTP(t *testing.T) {
	for _, identity := range []string{"member", "owner"} {
		for _, state := range []string{"live", "run cancelled", "missing grant", "revoked", "foreign run", "bad body", "forged actor", "store race", "private error"} {
			t.Run(identity+"/"+state, func(t *testing.T) {
				b := ownerBoundary()
				if identity == "member" {
					b = intakeBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsControl}
				}
				f := &engineTransactions{applied: runstate.Applied{CancelRequested: true, StateSeq: 9, Generation: 4, Attempt: 2}}
				body := `{"reason":"user requested"}`
				status, code, writes := 200, apierr.Code(""), 1
				switch state {
				case "run cancelled":
					b.snapshot.Binding.RunCancelled = true
				case "missing grant":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
					status, code, writes = 403, apierr.Forbidden, 0
				case "revoked":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
					status, code, writes = 401, apierr.Unauthenticated, 0
				case "foreign run":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "wrong run"}
					status, code, writes = 403, apierr.Forbidden, 0
				case "bad body":
					body = `null`
					status, code, writes = 400, apierr.BadRequest, 0
				case "forged actor":
					body = `{"reason":"user requested","actor":"forged"}`
					status, code, writes = 400, apierr.BadRequest, 0
				case "store race":
					f.err = &store.ErrIllegalTransition{Reason: store.ReasonHolderRights}
					status, code = 409, apierr.LockHeld
				case "private error":
					f.err = errors.New("private credential")
					status, code = 500, apierr.Internal
				}
				w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/cancel", body)
				if f.writes != writes {
					t.Fatalf("writes=%d want=%d: %d %s", f.writes, writes, w.Code, w.Body.String())
				}
				if code != "" {
					assertEngineError(t, w, status, code)
					return
				}
				var applied controllerwire.Applied
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &applied) != nil || !applied.CancelRequested || applied.StateSeq != 9 {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
				change, ok := f.nodeChange.(store.CancelNode)
				if !ok {
					t.Fatalf("cancel command=%T", f.nodeChange)
				}
				p := b.snapshot.Authorities[0].Principal
				kind := runstate.ActorRunHost
				if identity == "member" {
					kind = runstate.ActorOperator
				}
				if change.Ref != (runspec.NodeRef{RunID: "run", NodeID: "job"}) || change.Actor.Kind != kind || change.Actor.Authority != p.Authority || !reflect.DeepEqual(change.Result, &runstate.NodeResult{Outcome: runstate.NodeCancelled, Reason: "user requested"}) {
					t.Fatalf("change=%+v", change)
				}
				if identity == "owner" && (change.Actor.Holder != p.Capability.Binding.Holder || change.Actor.Generation != runspec.ClaimGeneration(p.Capability.Binding.Generation) || change.Actor.Owner != (runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: p.Capability.Binding.Holder, Generation: runspec.ClaimGeneration(p.Capability.Binding.Generation)}})) {
					t.Fatalf("owner fence=%+v", change.Actor)
				}
			})
		}
	}
}

func TestEngineFailureHTTP(t *testing.T) {
	for _, state := range []string{"persisted", "absent", "missing generation", "zero generation", "bad generation", "duplicate generation", "missing grant", "revoked", "cancelled", "undeclared recovery", "store race", "private error"} {
		t.Run(state, func(t *testing.T) {
			b := newCredentialBoundary()
			b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.DataRead}
			f := &engineTransactions{failure: &nodeproto.JobFailure{Job: "source", Stage: "step:build", Error: &apierr.Error{Code: apierr.SDKError, Message: "masked failure"}}}
			path := "/api/v1/runs/run/jobs/source/failure?generation=7"
			status, code, reads := 200, apierr.Code(""), 1
			switch state {
			case "absent":
				f.failure = nil
			case "missing generation":
				path = "/api/v1/runs/run/jobs/source/failure"
				status, code, reads = 400, apierr.BadRequest, 0
			case "zero generation":
				path = "/api/v1/runs/run/jobs/source/failure?generation=0"
				status, code, reads = 400, apierr.BadRequest, 0
			case "bad generation":
				path = "/api/v1/runs/run/jobs/source/failure?generation=secret"
				status, code, reads = 400, apierr.BadRequest, 0
			case "duplicate generation":
				path += "&generation=8"
				status, code, reads = 400, apierr.BadRequest, 0
			case "missing grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				status, code, reads = 403, apierr.Forbidden, 0
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				status, code, reads = 401, apierr.Unauthenticated, 0
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
				status, code, reads = 401, apierr.Unauthenticated, 0
			case "undeclared recovery":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "undeclared recovery"}
				status, code, reads = 403, apierr.Forbidden, 0
			case "store race":
				f.readErr = &store.ErrIllegalTransition{Reason: store.ReasonStaleGeneration}
				status, code = 409, apierr.LockHeld
			case "private error":
				f.readErr = errors.New("private credential")
				status, code = 500, apierr.Internal
			}
			w := snapshotRequest(expansionServer(t, b, f), b, "GET", path)
			if f.failureReads != reads || f.writes != 0 {
				t.Fatalf("reads=%d writes=%d: %d %s", f.failureReads, f.writes, w.Code, w.Body.String())
			}
			if code != "" {
				assertEngineError(t, w, status, code)
				return
			}
			var got *nodeproto.JobFailure
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, f.failure) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			if f.failureRef != (runspec.NodeRef{RunID: "run", NodeID: "source"}) || f.failureGeneration != 7 || f.failureActor.Authority != b.snapshot.Authorities[0].Principal.Authority || f.failureActor.Generation != 4 || f.failureActor.Holder != "runner" {
				t.Fatalf("read=%+v gen=%d actor=%+v", f.failureRef, f.failureGeneration, f.failureActor)
			}
			if b.bindingRule != capability.BindReadSet || b.bindingTarget.SourceGeneration != 7 || b.bindingTarget.NodeID != "source" {
				t.Fatalf("binding=%s target=%+v", b.bindingRule, b.bindingTarget)
			}
		})
	}
}

func TestEngineJobControlAuthenticatesBeforeDecodeErrors(t *testing.T) {
	b := newCredentialBoundary()
	b.raw = ""
	f := &engineTransactions{}
	w := snapshotRequest(expansionServer(t, b, f), b, "GET", "/api/v1/runs/run/jobs/source/failure?generation=secret")
	assertEngineError(t, w, 401, apierr.Unauthenticated)
	if f.failureReads != 0 || f.writes != 0 {
		t.Fatal("unauthenticated request reached transaction")
	}
}

func TestEngineJobCancelBodyRace(t *testing.T) {
	b, f := ownerBoundary(), &engineTransactions{}
	body := &reportBody{Reader: strings.NewReader(`{"reason":"user requested"}`), beforeRead: func() {
		b.snapshot.Binding.Current.Generation++
		f.err = &store.ErrIllegalTransition{Reason: store.ReasonStaleGeneration}
	}}
	r := httptest.NewRequest("POST", "/api/v1/runs/run/jobs/job/cancel", body)
	r.Header.Set("Authorization", "Bearer "+b.raw)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	expansionServer(t, b, f).ServeHTTP(w, r)
	assertEngineError(t, w, 409, apierr.LockHeld)
	change, ok := f.nodeChange.(store.CancelNode)
	if !ok {
		t.Fatalf("cancel command=%T", f.nodeChange)
	}
	if f.writes != 1 || change.Actor.Generation != 4 || change.Actor.Owner.Generation != 4 || !reflect.DeepEqual(change.Result, &runstate.NodeResult{Outcome: runstate.NodeCancelled, Reason: "user requested"}) {
		t.Fatalf("calls=%d change=%+v", f.writes, f.nodeChange)
	}
}

func TestEngineJobControlMethods(t *testing.T) {
	for _, tc := range []struct{ path, method string }{
		{"/api/v1/runs/run/jobs/job/cancel", "POST"}, {"/api/v1/runs/run/jobs/source/failure?generation=7", "GET"},
	} {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			if method == tc.method {
				continue
			}
			t.Run(tc.method+"/"+method, func(t *testing.T) {
				b, f := newCredentialBoundary(), &engineTransactions{}
				w := snapshotRequest(expansionServer(t, b, f), b, method, tc.path)
				assertEngineError(t, w, 405, apierr.MethodNotAllowed)
				if w.Header().Get("Allow") != tc.method || f.writes != 0 || f.failureReads != 0 {
					t.Fatalf("headers=%v", w.Header())
				}
			})
		}
	}
}
