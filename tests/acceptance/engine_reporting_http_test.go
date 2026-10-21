package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

var reportRequests = []struct{ suffix, body string }{
	{"annotations", `{"message":"masked"}`},
	{"summary", `{"markdown":"masked"}`},
	{"steps/annotations", `{"step_id":"compile","message":"masked"}`},
	{"steps/summary", `{"step_id":"compile","markdown":"masked"}`},
	{"steps/start", `{"step_id":"compile","state":"running","source_attempt":2}`},
	{"steps/finish", `{"step_id":"compile","state":"cancelled","source_attempt":2}`},
	{"steps/skip", `{"step_id":"compile","state":"skipped","source_attempt":2}`},
}

func TestEngineStepRejectsMalformedReports(t *testing.T) {
	for _, tc := range []struct{ operation, body string }{
		{"start", `{"step_id":"compile","state":"running","source_attempt":0}`},
		{"start", `{"step_id":"compile","state":"running","source_attempt":-1}`},
		{"start", `{"step_id":"compile","state":"passed","source_attempt":2}`},
		{"finish", `{"step_id":"compile","state":"running","source_attempt":2}`},
		{"skip", `{"step_id":"compile","state":"passed","source_attempt":2}`},
		{"finish", `{"step_id":"compile","state":"failed","source_attempt":2,"error":{}}`},
		{"finish", `{"step_id":"compile","state":"failed","source_attempt":2,"error":"` + strings.Repeat("x", int(limits.ErrorBytes)+1) + `"}`},
		{"start", `{}`},
		{"start", `{"step_id":"compile","status":"running"}`},
		{"start", `{"step_id":"compile","status":null}`},
		{"skip", `{"step_id":"compile","error":null}`},
		{"start", `{"step_id":"compile","error":{"code":"sdk_error","message":"masked"}}`},
		{"skip", `{"step_id":"compile","status":"skipped"}`},
		{"finish", `{"step_id":"compile"}`},
		{"finish", `{"step_id":"compile","status":"running"}`},
		{"finish", `{"step_id":"compile","status":"skipped"}`},
		{"finish", `{"step_id":"compile","status":"success"}`},
		{"finish", `{"step_id":"compile","status":"failed","error":{"error":{"code":"sdk_error","message":"masked"}}}`},
		{"finish", `{"step_id":"compile","status":"failed","error":{"message":"masked"}}`},
		{"finish", `{"step_id":"compile","status":"failed","error":{"code":"sdk_error","message":"` + strings.Repeat("x", int(limits.ErrorBytes)+1) + `"}}`},
		{"start", `{"step_id":"compile","attempt":999}`},
		{"start", `{"step_id":"compile","started_at":"2026-10-14T12:00:00Z"}`},
	} {
		t.Run(tc.operation+"/"+tc.body[:min(60, len(tc.body))], func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/steps/"+tc.operation, tc.body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if f.writes != 0 {
				t.Fatal("malformed report reached transaction")
			}
		})
	}
}

func TestEngineReportingTextBounds(t *testing.T) {
	for _, suffix := range []string{"annotations", "steps/annotations", "summary", "steps/summary"} {
		for _, size := range []int{int(limits.AnnotationBytes), int(limits.AnnotationBytes) + 1} {
			t.Run(suffix+"/"+strconv.Itoa(size), func(t *testing.T) {
				b, f := newCredentialBoundary(), &engineTransactions{}
				field, step := "message", ""
				if strings.HasSuffix(suffix, "summary") {
					field = "markdown"
				}
				if strings.HasPrefix(suffix, "steps/") {
					step = `"step_id":"compile",`
				}
				body := `{` + step + `"` + field + `":"` + strings.Repeat("x", size) + `"}`
				w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/"+suffix, body)
				if size > int(limits.AnnotationBytes) {
					assertEngineError(t, w, 413, apierr.PayloadTooLarge)
					if f.writes != 0 {
						t.Fatal("oversized text reached transaction")
					}
					return
				}
				if w.Code != http.StatusNoContent || f.writes != 1 || len(f.annotation.Message)+len(f.summary.Markdown) != size {
					t.Fatalf("HTTP %d %s, writes %d", w.Code, w.Body.String(), f.writes)
				}
			})
		}
	}
}

func TestEngineStepReadsPublisherAttempts(t *testing.T) {
	for _, name := range []string{"missing", "private error", "unstarted"} {
		t.Run(name, func(t *testing.T) {
			b := publisherBoundary()
			f := &engineTransactions{node: store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}}}
			status, code := 500, apierr.Internal
			switch name {
			case "missing":
				f.readErr = store.ErrNotFound
				status, code = 404, apierr.NotFound
			case "private error":
				f.readErr = errors.New("private credential")
			case "unstarted":
				status, code = 409, apierr.IllegalTransition
			}
			w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/steps/start", `{"step_id":"compile","state":"running","source_attempt":2}`)
			assertEngineError(t, w, status, code)
			if f.writes != 0 {
				t.Fatal("invalid attempt reached transaction")
			}
		})
	}
}

func TestEngineReportingMethodsAndHardCut(t *testing.T) {
	handler := controller.New(nil, nil).Handler()
	for _, operation := range reportRequests {
		for _, method := range []string{"GET", "HEAD", "PUT", "OPTIONS"} {
			t.Run(method+"/"+operation.suffix, func(t *testing.T) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/runs/run/jobs/job/"+operation.suffix, nil))
				assertEngineError(t, w, 405, apierr.MethodNotAllowed)
				if w.Header().Get("Allow") != "POST" {
					t.Fatalf("Allow = %q", w.Header().Get("Allow"))
				}
			})
		}
		t.Run("removed/"+operation.suffix, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/runs/run/nodes/job/"+operation.suffix, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("legacy path = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type reportTransport struct{ handler http.Handler }

func (tr reportTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	tr.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestEngineReportingAuthorities(t *testing.T) {
	for _, operation := range reportRequests {
		for _, authority := range []string{"job", "owner", "publisher", "cancelling"} {
			t.Run(operation.suffix+"/"+authority, func(t *testing.T) {
				b := newCredentialBoundary()
				f := &engineTransactions{node: store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Attempt: 11, Generation: 7}}
				kind, generation, attemptGeneration, attempt := store.ActorHolder, int64(4), int64(4), int64(2)
				switch authority {
				case "owner":
					b = ownerBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Binding.Attempt = 0
					b.snapshot.Binding.Current.Attempt = 0
					kind, attemptGeneration, attempt = store.ActorRunHost, 7, 11
				case "publisher":
					b = publisherBoundary()
					kind, generation, attemptGeneration, attempt = store.ActorOperator, 0, 7, 11
				case "cancelling":
					b.snapshot.Binding.RunCancelled = true
				}
				body := operation.body
				if strings.Contains(body, "source_attempt") {
					body = strings.ReplaceAll(body, `"source_attempt":2`, `"source_attempt":`+strconv.FormatInt(attempt, 10))
				}
				w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/"+operation.suffix, body)
				if authority == "owner" && strings.HasPrefix(operation.suffix, "steps/") {
					assertEngineError(t, w, http.StatusForbidden, apierr.Forbidden)
					if f.writes != 0 {
						t.Fatal("trigger owner reported a job step")
					}
					return
				}
				wantStatus := http.StatusNoContent
				if strings.Contains(body, "source_attempt") {
					wantStatus = http.StatusOK
				}
				if w.Code != wantStatus || f.writes != 1 {
					t.Fatalf("HTTP %d %s, writes %d", w.Code, w.Body.String(), f.writes)
				}
				actor := f.annotation.Actor
				switch operation.suffix {
				case "summary", "steps/summary":
					actor = f.summary.Actor
				case "steps/start", "steps/finish", "steps/skip":
					actor = f.step.Actor
					if f.step.Attempt.Attempt != attempt || f.step.Attempt.Generation != attemptGeneration {
						t.Fatalf("step fence = %+v", f.step.Attempt)
					}
				}
				if actor.Kind != kind || actor.Generation != runspec.ClaimGeneration(generation) || actor.Authority != b.snapshot.Authorities[0].Principal.Authority {
					t.Fatalf("actor = %+v", actor)
				}
				if authority == "owner" && actor.Owner != (store.OwnerLeaseRef{Kind: store.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: "runner", Generation: 4}}) {
					t.Fatalf("owner = %+v", actor.Owner)
				}
			})
		}
	}
}

func TestEngineReportingRefusesDeadOrWrongAuthority(t *testing.T) {
	for _, operation := range reportRequests {
		for _, name := range []string{"revoked", "missing grant", "other job", "body actor", "transaction race"} {
			t.Run(operation.suffix+"/"+name, func(t *testing.T) {
				b, f := newCredentialBoundary(), &engineTransactions{}
				path, body := "/api/v1/runs/run/jobs/job/"+operation.suffix, operation.body
				status, code, writes := 401, apierr.Unauthenticated, 0
				switch name {
				case "revoked":
					b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "missing grant":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
					status, code = 403, apierr.Forbidden
				case "other job":
					path = "/api/v1/runs/run/jobs/sibling/" + operation.suffix
					status, code = 403, apierr.Forbidden
				case "body actor":
					body = strings.TrimSuffix(body, "}") + `,"actor":"forged"}`
					status, code = 400, apierr.BadRequest
				case "transaction race":
					f.err = &store.ErrIllegalTransition{Reason: store.ReasonHolderRights}
					status, code, writes = 409, apierr.LockHeld, 1
				}
				w := engineHTTP(t, b, f, path, body)
				assertEngineError(t, w, status, code)
				if f.writes != writes {
					t.Fatalf("writes = %d, want %d", f.writes, writes)
				}
			})
		}
	}
}

type reportBody struct {
	io.Reader
	beforeRead func()
}

func (b *reportBody) Read(p []byte) (int, error) {
	if b.beforeRead != nil {
		b.beforeRead()
		b.beforeRead = nil
	}
	return b.Reader.Read(p)
}

func TestEngineReportingBodyRace(t *testing.T) {
	for _, operation := range reportRequests {
		t.Run(operation.suffix, func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			body := &reportBody{Reader: strings.NewReader(operation.body), beforeRead: func() {
				b.snapshot.Binding.Current.Generation = 5
				f.err = &store.ErrIllegalTransition{Reason: store.ReasonStaleGeneration}
			}}
			r := httptest.NewRequest("POST", "/api/v1/runs/run/jobs/job/"+operation.suffix, body)
			r.Header.Set("Authorization", "Bearer "+b.raw)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			expansionServer(t, b, f).ServeHTTP(w, r)
			assertEngineError(t, w, 409, apierr.LockHeld)
			if f.writes != 1 {
				t.Fatalf("transaction calls = %d", f.writes)
			}
			actor := f.annotation.Actor
			switch operation.suffix {
			case "summary", "steps/summary":
				actor = f.summary.Actor
			case "steps/start", "steps/finish", "steps/skip":
				actor = f.step.Actor
			}
			if actor.Generation != 4 {
				t.Fatalf("original fence was replaced: %+v", actor)
			}
		})
	}
}

func TestEngineAnnotationHTTP(t *testing.T) {
	b, f := newCredentialBoundary(), &engineTransactions{}
	w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/annotations", `{"message":"masked annotation","level":"warning","file":"build.go","line":12}`)
	if w.Code != http.StatusNoContent || f.writes != 1 {
		t.Fatalf("HTTP %d %s, writes %d", w.Code, w.Body.String(), f.writes)
	}
	a := f.annotation
	if a.Ref != (controllerwire.NodeRef{RunID: "run", NodeID: "job"}) || a.Actor.Authority != b.snapshot.Authorities[0].Principal.Authority ||
		a.Actor.Kind != store.ActorHolder || a.Actor.Holder != "runner" || a.Actor.Generation != 4 || a.Actor.CapabilityPrefix != "claim" ||
		a.StepID != "" || a.Message != "masked annotation" || a.Level != "warning" || a.File != "build.go" || a.Line == nil || *a.Line != 12 {
		t.Fatalf("annotation = %+v", a)
	}
}

func TestEngineSummaryHTTP(t *testing.T) {
	for _, scope := range []string{"job", "step"} {
		t.Run(scope, func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			path, body, step := "/api/v1/runs/run/jobs/job/summary", `{"markdown":"## masked summary"}`, ""
			if scope == "step" {
				path, body, step = "/api/v1/runs/run/jobs/job/steps/summary", `{"step_id":"compile","markdown":"## masked summary"}`, "compile"
			}
			w := engineHTTP(t, b, f, path, body)
			if w.Code != http.StatusNoContent || f.writes != 1 || f.summary.Ref != (controllerwire.NodeRef{RunID: "run", NodeID: "job"}) ||
				f.summary.Actor.Authority != b.snapshot.Authorities[0].Principal.Authority || f.summary.Actor.Generation != 4 ||
				f.summary.StepID != step || f.summary.Markdown != "## masked summary" {
				t.Fatalf("HTTP %d %s, summary %+v", w.Code, w.Body.String(), f.summary)
			}
		})
	}
}

func TestEngineStepHTTP(t *testing.T) {
	for _, operation := range []string{"start", "passed", "failed", "cancelled", "skip"} {
		t.Run(operation, func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			suffix, body, state := operation, `{"step_id":"compile","state":"running","source_attempt":2}`, "running"
			switch operation {
			case "passed", "failed", "cancelled":
				suffix, body, state = "finish", `{"step_id":"compile","state":"`+operation+`","source_attempt":2,"error":"masked failure"}`, operation
			case "skip":
				state, body = "skipped", `{"step_id":"compile","state":"skipped","source_attempt":2}`
			}
			w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/steps/"+suffix, body)
			s := f.step
			if w.Code != http.StatusOK || f.writes != 1 || s.StepID != "compile" || s.State != state ||
				s.Attempt != (runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 2, Generation: 4}) ||
				s.Actor.Authority != b.snapshot.Authorities[0].Principal.Authority || s.Actor.Holder != "runner" || s.Actor.Generation != 4 ||
				s.StartedAt != nil || s.FinishedAt != nil {
				t.Fatalf("HTTP %d %s, step %+v", w.Code, w.Body.String(), s)
			}
			if suffix == "finish" && s.MaskedError != "masked failure" {
				t.Fatalf("error = %q", s.MaskedError)
			}
		})
	}
}

func TestEngineAnnotationRefusals(t *testing.T) {
	for _, name := range []string{"anonymous", "revoked parent", "ended", "stale generation", "wrong holder", "missing grant", "claimless root", "other job", "other run", "body actor", "body generation", "job route step", "trailing body", "null", "oversized", "transaction race", "private error"} {
		t.Run(name, func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			path, body := "/api/v1/runs/run/jobs/job/annotations", `{"message":"masked"}`
			status, code, writes := 401, apierr.Unauthenticated, 0
			switch name {
			case "anonymous":
				b.raw = ""
			case "revoked parent":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "ended":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "stale generation":
				b.snapshot.Binding.Current.Generation++
			case "wrong holder":
				b.snapshot.Binding.Current.Holder = "replacement"
			case "missing grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "claimless root":
				b = intakeBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.NodeReport}
				status, code = 403, apierr.Forbidden
			case "other job":
				path = "/api/v1/runs/run/jobs/sibling/annotations"
				status, code = 403, apierr.Forbidden
			case "other run":
				path = "/api/v1/runs/other/jobs/job/annotations"
				status, code = 403, apierr.Forbidden
			case "body actor":
				body = `{"message":"masked","actor":"forged"}`
				status, code = 400, apierr.BadRequest
			case "body generation":
				body = `{"message":"masked","generation":999}`
				status, code = 400, apierr.BadRequest
			case "job route step":
				body = `{"message":"masked","step_id":"step"}`
				status, code = 400, apierr.BadRequest
			case "trailing body":
				body += `{}`
				status, code = 400, apierr.BadRequest
			case "null":
				body = `null`
				status, code = 400, apierr.BadRequest
			case "oversized":
				body = `{"message":"` + strings.Repeat("x", int(limits.AnnotationBytes)+1) + `"}`
				status, code = 413, apierr.PayloadTooLarge
			case "transaction race":
				f.err = &store.ErrIllegalTransition{Reason: store.ReasonStaleGeneration}
				status, code, writes = 409, apierr.LockHeld, 1
			case "private error":
				f.err = errors.New("private credential")
				status, code, writes = 500, apierr.Internal, 1
			}
			w := engineHTTP(t, b, f, path, body)
			assertEngineError(t, w, status, code)
			if f.writes != writes {
				t.Fatalf("writes = %d, want %d", f.writes, writes)
			}
		})
	}
}

func TestEngineStepAnnotationHTTP(t *testing.T) {
	for _, step := range []string{"compile", ""} {
		t.Run(step, func(t *testing.T) {
			b, f := newCredentialBoundary(), &engineTransactions{}
			w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/steps/annotations", `{"step_id":"`+step+`","message":"step note"}`)
			if step == "" {
				assertEngineError(t, w, 400, apierr.BadRequest)
				if f.writes != 0 {
					t.Fatal("missing step reached persistence")
				}
				return
			}
			if w.Code != http.StatusNoContent || f.writes != 1 || f.annotation.StepID != step || f.annotation.Message != "step note" {
				t.Fatalf("HTTP %d %s, annotation %+v", w.Code, w.Body.String(), f.annotation)
			}
		})
	}
}

func TestEngineTypedStepHTTP(t *testing.T) {
	b, f := newCredentialBoundary(), &engineTransactions{}
	h := expansionServer(t, b, f)
	c, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", HTTP: &http.Client{Transport: reportTransport{h}}, Bearer: func(context.Context) (string, error) { return b.raw, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"running", "passed", "failed", "cancelled", "skipped"} {
		input := controllerwire.StepReportRequest{StepID: "compile", State: state, SourceAttempt: 2, MaskedError: "masked"}
		if err := c.RecordStep(t.Context(), "run", "job", input); err != nil {
			t.Fatal(err)
		}
		if f.step.Attempt.Attempt != 2 || f.step.Actor.Generation != 4 || f.step.State != state || f.step.MaskedError != "masked" {
			t.Fatalf("step=%+v", f.step)
		}
	}
	if f.writes != 5 {
		t.Fatalf("writes=%d", f.writes)
	}
}

func TestEngineTypedStepAttemptProperty(t *testing.T) {
	check := func(seed uint32, delayed bool) bool {
		observed := int64(seed) + 1
		for _, publisher := range []bool{false, true} {
			b := newCredentialBoundary()
			if publisher {
				b = publisherBoundary()
			}
			if !publisher {
				b.snapshot.Authorities[0].Principal.Capability.Binding.Attempt = observed
				b.snapshot.Binding.Current.Attempt = observed
			}
			current := observed
			if delayed {
				current++
			}
			if !publisher {
				b.snapshot.Authorities[0].Principal.Capability.Binding.Attempt = current
				b.snapshot.Binding.Current.Attempt = current
			}
			f := &engineTransactions{node: store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Generation: 7, Attempt: runspec.AttemptOrdinal(current)}}
			start := time.Date(2026, 10, 14, 12, 0, 0, int(seed%1000000000), time.UTC)
			finish := start.Add(time.Nanosecond)
			input := controllerwire.StepReportRequest{StepID: "compile", State: "passed", SourceAttempt: observed, StartedAt: &start, FinishedAt: &finish, MaskedError: "masked"}
			body, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			w := engineHTTP(t, b, f, "/api/v1/runs/run/jobs/job/steps/finish", string(body))
			if delayed {
				if w.Code != 409 || f.writes != 0 {
					t.Logf("delayed HTTP %d %s writes=%d", w.Code, w.Body, f.writes)
					return false
				}
				continue
			}
			if w.Code != 200 || f.writes != 1 || f.step.Attempt.Attempt != observed || f.step.StartedAt == nil || !f.step.StartedAt.Equal(start) || f.step.FinishedAt == nil || !f.step.FinishedAt.Equal(finish) || f.step.Actor.Authority != b.snapshot.Authorities[0].Principal.Authority {
				t.Logf("HTTP %d %s report=%+v", w.Code, w.Body, f.step)
				return false
			}
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
