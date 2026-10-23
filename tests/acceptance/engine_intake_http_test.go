package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func intakeBoundary() *credentialBoundary {
	b := newCredentialBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "account"}
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = "", "", ""
	p.Capability.Parent = nil
	p.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
	p.Capability.Grants = []capability.Grant{capability.RunsWrite}
	b.snapshot.Authorities = b.snapshot.Authorities[:1]
	b.snapshot.Binding.Current = p.Capability.Binding
	return b
}

func planBoundary() *credentialBoundary {
	b := newCredentialBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = capability.UnitPlan, "plan", capability.PurposePlan
	p.Capability.Binding.Kind, p.Capability.Binding.NodeID = capability.BindingRun, ""
	b.snapshot.Binding.Current = p.Capability.Binding
	return b
}

func engineHTTP(t *testing.T, b *credentialBoundary, f *engineTransactions, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Sparkwing-Generation", "999")
	r.Header.Set("X-Sparkwing-Holder", "forged")
	w := httptest.NewRecorder()
	expansionServer(t, b, f).ServeHTTP(w, r)
	return w
}

func assertEngineError(t *testing.T, w *httptest.ResponseRecorder, status int, code apierr.Code) {
	t.Helper()
	var envelope apierr.Envelope
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error == nil || envelope.Error.Code != code || strings.Contains(w.Body.String(), "private credential") {
		t.Fatalf("HTTP %d %s, want %d %s", w.Code, w.Body.String(), status, code)
	}
}

func TestEngineIntakeHTTP(t *testing.T) {
	for _, name := range []string{"member", "replay", "child", "wrong parent", "sibling parent job", "foreign team", "read only", "anonymous", "revoked", "unknown field", "trailing body", "null", "old wire", "transaction refusal", "source removed", "private error"} {
		t.Run(name, func(t *testing.T) {
			b := intakeBoundary()
			f := &engineTransactions{admitted: store.AdmissionResult{RunID: "created", Number: 19, HereBearer: []byte("here-once")}}
			sourceFields := `"git":{"repository":{"key":"git:example.com/team/repo"},"ref":"main","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"source":{"ref":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","location":{"remote_url":"https://example.com/team/repo.git","pipeline_dir":".sparkwing"}}`
			body := `{"request":{"version":1,"team":"team","pipeline":"build","mode":"execute","origin":"trigger","dispatch":"host",` + sourceFields + `,"actor":{"kind":"operator","id":"forged"}},"idempotency_key":"once"}`
			status, code, writes := http.StatusCreated, apierr.Code(""), 1
			switch name {
			case "replay":
				f.admitted.Replayed, f.admitted.HereBearer = true, nil
				status = http.StatusOK
			case "child", "wrong parent", "sibling parent job":
				b = newCredentialBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.ChildrenStart}
				b.snapshot.Authorities[0].Principal.Capability.Children = []nodeproto.ChildPipeline{{Pipeline: "build", Repository: "git:example.com/team/repo"}}
				// A child runs from its parent's repository location and revision.
				var from runspec.Request
				if err := json.Unmarshal([]byte(`{`+sourceFields+`}`), &from); err != nil {
					t.Fatal(err)
				}
				f.run.Request.Request.Git, f.run.Request.Request.Source = from.Git, from.Source
				parent := "run"
				job := "job"
				if name == "wrong parent" {
					parent, status, code, writes = "other", 403, apierr.Forbidden, 0
				}
				if name == "sibling parent job" {
					job, status, code, writes = "sibling", 403, apierr.Forbidden, 0
				}
				body = `{"request":{"version":1,"team":"team","pipeline":"build","mode":"execute","origin":"child","dispatch":"host",` + sourceFields + `,"lineage":{"parent_run_id":"` + parent + `","parent_job_id":"` + job + `"}},"idempotency_key":"once"}`
			case "foreign team":
				body = strings.Replace(body, `"team":"team"`, `"team":"foreign"`, 1)
				status, code, writes = 403, apierr.Forbidden, 0
			case "read only":
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
				status, code, writes = 403, apierr.Forbidden, 0
			case "anonymous":
				b.raw = ""
				status, code, writes = 401, apierr.Unauthenticated, 0
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				status, code, writes = 401, apierr.Unauthenticated, 0
			case "unknown field":
				body = `{"request":{"team":"team","pipeline":"build"},"authority":"forged"}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "trailing body":
				body += `{}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "null":
				body = `null`
				status, code, writes = 400, apierr.BadRequest, 0
			case "old wire":
				body = `{"id":"old-upsert","pipeline":"build","status":"running"}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "transaction refusal":
				f.err = &apierr.Error{Code: apierr.LockHeld, Message: "intake authority changed"}
				status, code = 409, apierr.LockHeld
			case "source removed":
				f.err = store.ErrNotFound
				status, code = 404, apierr.NotFound
			case "private error":
				f.err = errors.New("private credential")
				status, code = 500, apierr.Internal
			}
			w := engineHTTP(t, b, f, "/api/v1/runs", body)
			if f.writes != writes {
				t.Fatalf("writes = %d, want %d; HTTP %d %s", f.writes, writes, w.Code, w.Body.String())
			}
			if code != "" {
				assertEngineError(t, w, status, code)
				return
			}
			var response controllerwire.CreateRunResponse
			if w.Code != status || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.RunID != "created" || response.Number != 19 || response.Replayed != f.admitted.Replayed || response.HereBearer != string(f.admitted.HereBearer) {
				t.Fatalf("intake response = %d %s", w.Code, w.Body.String())
			}
			if f.intake.Authority != b.snapshot.Authorities[0].Principal.Authority || f.intake.Request.Actor.ID == "forged" || f.intake.IdempotencyKey != "once" {
				t.Fatalf("intake transaction = %+v", f.intake)
			}
			if f.intake.Request.Git.Repository.Key != "git:example.com/team/repo" || f.intake.Request.Source.Location.RemoteURL != "https://example.com/team/repo.git" || f.intake.Request.Source.Location.PipelineDir != ".sparkwing" || f.intake.Request.Source.Ref != f.intake.Request.Git.SHA {
				t.Fatalf("intake source = %+v", f.intake.Request)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("one-time bearer response may be cached")
			}
		})
	}
}

func TestEnginePlanHTTP(t *testing.T) {
	for _, name := range []string{"planner", "owner", "work claim", "sibling run", "cancelled", "ended", "exact replay", "changed replay", "wrong route replay", "body fence", "trailing body", "old wire", "transaction refusal", "first seal removed"} {
		t.Run(name, func(t *testing.T) {
			b := planBoundary()
			f := &engineTransactions{
				run:      store.RunRow{Team: "team", Execution: runstate.Execution{ID: "run", Request: runspec.SealedRequest{RunID: "run", Hash: "first-seal"}}},
				accepted: store.AcceptedPlan{PlanHash: "committed", Plan: json.RawMessage(`{"jobs":[]}`)},
			}
			path, body := "/api/v1/runs/run/plan", `{"plan":{"jobs":[]},"declarations":{"version":1,"file":"sparkwing.yaml"}}`
			status, code, writes := 200, apierr.Code(""), 1
			switch name {
			case "owner":
				p := &b.snapshot.Authorities[0].Principal
				p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = capability.UnitTrigger, "trigger", capability.PurposeTrigger
				p.Capability.Binding.Kind = capability.BindingTrigger
				b.snapshot.Binding.Current = p.Capability.Binding
			case "work claim":
				b = newCredentialBoundary()
				status, code, writes = 403, apierr.Forbidden, 0
			case "sibling run":
				path = "/api/v1/runs/sibling/plan"
				status, code, writes = 403, apierr.Forbidden, 0
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
			case "ended", "exact replay", "changed replay", "wrong route replay":
				finished := b.snapshot.Now.Add(-time.Second)
				b.snapshot.Binding.FinishedAt = &finished
				status, code, writes = 401, apierr.Unauthenticated, 0
				if name != "ended" {
					digest := sha256.Sum256([]byte(body))
					b.snapshot.Binding.Result = &capability.RecordedResult{
						Actor:  b.snapshot.Authorities[0].Principal.Authority,
						Method: "POST", Pattern: "/api/v1/runs/{run}/plan", RunID: "run", Generation: 4, Attempt: 2, Digest: hex.EncodeToString(digest[:]),
					}
					if name == "exact replay" {
						status, code, writes, f.accepted.Replayed = 200, "", 1, true
					} else if name == "changed replay" {
						body += " "
					} else {
						b.snapshot.Binding.Result.Pattern = "/api/v1/runs/{run}/finish"
					}
				}
			case "body fence":
				body = `{"plan":{"jobs":[]},"generation":999}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "trailing body":
				body += `{}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "old wire":
				body = `{"jobs":[]}`
				status, code, writes = 400, apierr.BadRequest, 0
			case "transaction refusal":
				f.err = &apierr.Error{Code: apierr.SpecDrift, Message: "declarations changed"}
				status, code = 409, apierr.SpecDrift
			case "first seal removed":
				f.runReadErr = store.ErrNotFound
				status, code, writes = 404, apierr.NotFound, 0
			}
			w := engineHTTP(t, b, f, path, body)
			if f.writes != writes {
				t.Fatalf("writes = %d, want %d; HTTP %d %s", f.writes, writes, w.Code, w.Body.String())
			}
			if code != "" {
				assertEngineError(t, w, status, code)
				return
			}
			var response controllerwire.PlanAccepted
			if w.Code != status || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.PlanHash != "committed" || response.Replayed != f.accepted.Replayed || response.Notices == nil {
				t.Fatalf("plan response = %d %s", w.Code, w.Body.String())
			}
			if f.plan.Actor.Generation != 4 || f.plan.Actor.Holder != "runner" || f.plan.Seal.Request.Hash != "first-seal" || f.plan.Seal.Declarations.File != "sparkwing.yaml" || !f.plan.Seal.Now.IsZero() {
				t.Fatalf("plan transaction = %+v", f.plan)
			}
		})
	}
}
