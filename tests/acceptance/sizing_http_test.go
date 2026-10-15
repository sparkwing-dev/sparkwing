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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sizingStore struct {
	authorization            capability.Authorized
	profile                  *store.PipelineProfile
	err                      error
	reads, writes            int
	pipeline, job, operation string
	cores                    float64
	memory                   int64
	observation              store.ProfileObservation
	wait                     time.Duration
}

func (f *sizingStore) GetPipelineProfile(_ context.Context, pipeline, job string) (*store.PipelineProfile, error) {
	f.pipeline, f.job = pipeline, job
	f.reads++
	return f.profile, f.err
}

func (f *sizingStore) SetProfilePin(_ context.Context, a capability.Authorized, cores float64, memory int64) error {
	f.authorization = a
	f.operation = "pin"
	if cores == 0 && memory == 0 {
		f.operation = "clear"
	}
	f.pipeline, f.job, f.cores, f.memory = a.Target.Pipeline, a.Target.NodeID, cores, memory
	f.writes++
	return f.err
}

func (f *sizingStore) RecordProfileObservation(_ context.Context, a capability.Authorized, observation store.ProfileObservation) error {
	f.authorization = a
	f.operation, f.pipeline, f.job, f.observation = "observations", a.Target.Pipeline, a.Target.NodeID, observation
	f.writes++
	return f.err
}

func (f *sizingStore) RecordContention(_ context.Context, a capability.Authorized) error {
	f.authorization = a
	f.operation, f.pipeline = "contention", a.Target.Pipeline
	f.writes++
	return f.err
}

func (f *sizingStore) RecordWaitObservation(_ context.Context, a capability.Authorized, wait time.Duration) error {
	f.authorization = a
	f.operation, f.pipeline, f.wait = "waits", a.Target.Pipeline, wait
	f.writes++
	return f.err
}

func sizingServer(t *testing.T, b *credentialBoundary, f *sizingStore) http.Handler {
	t.Helper()
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
	s.WithSizingStore(func(_ context.Context, team store.Team) (controller.SizingStore, error) {
		if team != "team" {
			t.Fatalf("sizing team = %q", team)
		}
		return f, nil
	})
	return s.Handler()
}

func sizingRequest(h http.Handler, b *credentialBoundary, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if b.raw != "" {
		r.Header.Set("Authorization", "Bearer "+b.raw)
	}
	r.Header.Set("X-Sparkwing-Generation", "999")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSizingHTTPReads(t *testing.T) {
	for _, pipeline := range []string{"build", "ci/build", "3:abcbuild"} {
		for _, class := range []string{"work", "trigger", "plan", "member", "missing"} {
			t.Run(pipeline+"/"+class, func(t *testing.T) {
				b := newCredentialBoundary()
				switch class {
				case "trigger":
					b = ownerBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.SourceRead)
				case "plan":
					b = planBoundary()
				case "member":
					b = authorityMember()
				}
				f := &sizingStore{profile: &store.PipelineProfile{Pipeline: pipeline, NodeID: "job", PinnedCores: 1.5}}
				if class == "missing" {
					f.profile = nil
				}
				path := "/api/v1/pipelines/" + url.PathEscape(pipeline) + "/sizing?job=job&team=forged"
				w := sizingRequest(sizingServer(t, b, f), b, "GET", path, "")
				if class == "missing" {
					assertEngineError(t, w, 404, apierr.NotFound)
				} else {
					var got store.PipelineProfile
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if w.Code != 200 || got.PinnedCores != 1.5 {
						t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
					}
				}
				if f.reads != 1 || f.writes != 0 || f.pipeline != pipeline || f.job != "job" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("read = %+v; headers=%v", f, w.Header())
				}
				if class != "member" && (b.bindingRule != capability.BindPipelineOfBoundRun || b.bindingTarget.Pipeline != pipeline || b.bindingTarget.NodeID != "job") {
					t.Fatalf("binding=%s target=%+v", b.bindingRule, b.bindingTarget)
				}
			})
		}
	}
}

func TestSizingHTTPWrites(t *testing.T) {
	for _, tc := range []struct{ method, leaf, body, operation string }{
		{"PUT", "pin", `{"cores":1.5,"memory_bytes":2048}`, "pin"},
		{"PUT", "pin", `{"cores":0,"memory_bytes":0}`, "clear"},
		{"POST", "observations", `{"peak_cores":2.5,"peak_memory_bytes":4096,"duration_nanos":10,"cpu_measured":true,"partial":true,"plan_hash":"hash"}`, "observations"},
		{"POST", "contention", `{}`, "contention"},
		{"POST", "waits", `{"wait_nanos":23}`, "waits"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			b, f := newCredentialBoundary(), &sizingStore{}
			w := sizingRequest(sizingServer(t, b, f), b, tc.method, "/api/v1/pipelines/ci%2Fbuild/sizing/"+tc.leaf+"?job=job&team=forged", tc.body)
			if w.Code != 204 || f.writes != 1 || f.reads != 0 || f.pipeline != "ci/build" || f.operation != tc.operation {
				t.Fatalf("HTTP %d %s; write=%+v", w.Code, w.Body.String(), f)
			}
			if f.authorization.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || f.authorization.Binding.Generation != 4 {
				t.Fatalf("write authority=%+v", f.authorization)
			}
			if b.bindingRule != capability.BindPipelineOfBoundRun || b.bindingTarget.Pipeline != "ci/build" {
				t.Fatalf("binding=%+v", b.bindingTarget)
			}
			switch tc.operation {
			case "pin":
				if f.cores != 1.5 || f.memory != 2048 || f.job != "job" {
					t.Fatalf("pin=%+v", f)
				}
			case "observations":
				if f.observation.PeakCores != 2.5 || f.observation.Duration != 10 || !f.observation.Partial || f.job != "job" {
					t.Fatalf("observation=%+v", f)
				}
			case "waits":
				if f.wait != 23 {
					t.Fatalf("wait=%s", f.wait)
				}
			}
		})
	}
}

func TestSizingHTTPRefusals(t *testing.T) {
	for _, state := range []string{"absent", "malformed", "revoked", "parent revoked", "membership lost", "lease expired", "stale generation", "wrong holder", "finished", "cancelled", "missing grant", "bare machine", "actions root", "bootstrap", "eval", "nodehost", "other pipeline", "foreign repo", "private store"} {
		t.Run(state, func(t *testing.T) {
			b, f := newCredentialBoundary(), &sizingStore{}
			status, code := 401, apierr.Unauthenticated
			switch state {
			case "absent":
				b.raw = ""
			case "malformed":
				b.raw = "bad"
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "parent revoked":
				b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "membership lost":
				b.snapshot.Authorities[0].MembershipValid = false
			case "lease expired":
				b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
			case "stale generation":
				b.snapshot.Binding.Current.Generation++
			case "wrong holder":
				b.snapshot.Binding.Current.Holder = "other"
			case "finished":
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
			case "cancelled":
				b.snapshot.Binding.RunCancelled = true
				status, code = 403, apierr.Forbidden
			case "missing grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "bare machine", "actions root":
				b = authorityMember()
				b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.NodesClaim, capability.SourceRead}
				if state == "actions root" {
					p := &b.snapshot.Authorities[0].Principal.Capability
					p.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "123"}}
				}
				status, code = 403, apierr.Forbidden
			case "bootstrap":
				b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
				status, code = 403, apierr.Forbidden
			case "eval":
				p := &b.snapshot.Authorities[0].Principal
				p.Capability.Unit, p.Capability.Purpose = capability.UnitEval, capability.PurposeEval
				p.Capability.Grants = []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
				p.Capability.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "unit", Holder: "eval-host", Generation: 1}
				expires := b.snapshot.Now.Add(time.Minute)
				b.snapshot.Binding.FinishedAt = &b.snapshot.Now
				b.snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *p.Capability.Binding.Expansion, ExpiresAt: expires, ControllerLeaseExpiresAt: &expires}
				status, code = 403, apierr.Forbidden
			case "nodehost":
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
				b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
				b.snapshot.Binding.ProcessAlive = true
				status, code = 403, apierr.Forbidden
			case "other pipeline", "foreign repo":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "binding refused", Details: map[string]any{"reason": apierr.ReasonBinding}}
				status, code = 403, apierr.Forbidden
			case "private store":
				f.err = errors.New("private store data")
				status, code = 500, apierr.Internal
			}
			w := sizingRequest(sizingServer(t, b, f), b, "GET", "/api/v1/pipelines/other/sizing", "")
			assertEngineError(t, w, status, code)
			reads := 0
			if state == "private store" {
				reads = 1
			}
			if f.reads != reads || f.writes != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("reads=%d writes=%d body=%s", f.reads, f.writes, w.Body.String())
			}
		})
	}
}

func TestSizingHTTPWriteRefusals(t *testing.T) {
	for _, leaf := range []string{"pin", "observations", "contention", "waits"} {
		for _, state := range []string{"member", "admin", "revoked", "binding", "transaction race", "private store"} {
			t.Run(leaf+"/"+state, func(t *testing.T) {
				b, f := newCredentialBoundary(), &sizingStore{}
				status, code, writes := 403, apierr.Forbidden, 0
				switch state {
				case "member":
					b = authorityMember()
				case "admin":
					b = authorityMember()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TeamAdmin, capability.NodeReport}
				case "revoked":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
					status, code = 401, apierr.Unauthenticated
				case "binding":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "wrong sealed pipeline"}
				case "transaction race":
					f.err = &store.ErrIllegalTransition{Reason: store.ReasonStaleGeneration}
					status, code, writes = 409, apierr.LockHeld, 1
				case "private store":
					f.err = errors.New("private write failed")
					status, code, writes = 500, apierr.Internal, 1
				}
				method := "POST"
				if leaf == "pin" {
					method = "PUT"
				}
				w := sizingRequest(sizingServer(t, b, f), b, method, "/api/v1/pipelines/build/sizing/"+leaf, `{}`)
				assertEngineError(t, w, status, code)
				if f.writes != writes || f.reads != 0 || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("writes=%d reads=%d body=%s", f.writes, f.reads, w.Body.String())
				}
			})
		}
	}
}

func TestSizingHTTPInvalidMeasurements(t *testing.T) {
	for _, tc := range []struct{ method, leaf, body string }{
		{"PUT", "pin", `{"cores":-1}`},
		{"PUT", "pin", `{"cores":1e9}`},
		{"PUT", "pin", `{"memory_bytes":-1}`},
		{"PUT", "pin", `{"actor":"forged"}`},
		{"PUT", "pin", `{"generation":999}`},
		{"PUT", "pin", `{}` + `{}`},
		{"PUT", "pin", `null`},
		{"POST", "observations", `{"peak_cores":-1}`},
		{"POST", "observations", `{"peak_memory_bytes":-1}`},
		{"POST", "observations", `{"duration_nanos":-1}`},
		{"POST", "observations", `{"floor_cores":-1}`},
		{"POST", "observations", `{"floor_memory_bytes":-1}`},
		{"POST", "observations", `{"sustained_cores":-1}`},
		{"POST", "observations", `{"peak_cores":1e100}`},
		{"POST", "observations", `{"holder":"other"}`},
		{"POST", "waits", `{"wait_nanos":-1}`},
		{"POST", "waits", `{"wait_nanos":9223372036854775807}`},
		{"POST", "waits", `[]`},
	} {
		t.Run(tc.leaf+"/"+tc.body, func(t *testing.T) {
			b, f := newCredentialBoundary(), &sizingStore{}
			w := sizingRequest(sizingServer(t, b, f), b, tc.method, "/api/v1/pipelines/build/sizing/"+tc.leaf, tc.body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if f.writes != 0 || f.reads != 0 {
				t.Fatalf("store reached: %+v", f)
			}
		})
	}
}

func TestSizingHTTPMissingStoreAndMethods(t *testing.T) {
	for _, state := range []string{"no store", "nil store", "store error", "no policy"} {
		t.Run(state, func(t *testing.T) {
			b := newCredentialBoundary()
			s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
			code := apierr.PolicyMissing
			switch state {
			case "nil store":
				s.WithSizingStore(func(context.Context, store.Team) (controller.SizingStore, error) { return nil, nil })
			case "store error":
				s.WithSizingStore(func(context.Context, store.Team) (controller.SizingStore, error) {
					return nil, errors.New("private resolver")
				})
				code = apierr.Internal
			case "no policy":
				s.WithAuthorization(controller.RouteAuthorization{})
			}
			for _, method := range []string{"GET", "PUT"} {
				path := "/api/v1/pipelines/build/sizing"
				if method == "PUT" {
					path += "/pin"
				}
				w := sizingRequest(s.Handler(), b, method, path, `{}`)
				assertEngineError(t, w, apierr.Status(code), code)
				if strings.Contains(w.Body.String(), "private") {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
	for _, leaf := range []string{"", "/pin", "/observations", "/contention", "/waits"} {
		b, f := newCredentialBoundary(), &sizingStore{}
		h := sizingServer(t, b, f)
		allow := "POST"
		if leaf == "" {
			allow = "GET"
		}
		if leaf == "/pin" {
			allow = "PUT"
		}
		for _, method := range []string{"HEAD", "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			if method == allow {
				continue
			}
			w := sizingRequest(h, b, method, "/api/v1/pipelines/build/sizing"+leaf, `{}`)
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
			if w.Header().Get("Allow") != allow {
				t.Fatalf("Allow=%q want %s", w.Header().Get("Allow"), allow)
			}
		}
		w := sizingRequest(h, b, allow, "/api/v1/pipelines/build/profile"+leaf, `{}`)
		if w.Code != 404 {
			t.Fatalf("removed route HTTP %d %s", w.Code, w.Body.String())
		}
		if f.writes != 0 || f.reads != 0 {
			t.Fatalf("store reached: %+v", f)
		}
	}
}
