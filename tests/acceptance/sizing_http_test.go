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
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type sizingStore struct {
	profile       *store.PipelineProfile
	err           error
	reads         int
	pipeline, job string
}

func (f *sizingStore) GetPipelineProfile(_ context.Context, pipeline, job string) (*store.PipelineProfile, error) {
	f.pipeline, f.job = pipeline, job
	f.reads++
	return f.profile, f.err
}

func sizingServer(t *testing.T, b *credentialBoundary, f *sizingStore) http.Handler {
	t.Helper()
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
	bindTenantPort[*sizingStore](s, func(_ context.Context, team store.Team) (*sizingStore, error) {
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
				f := &sizingStore{profile: &store.PipelineProfile{Pipeline: pipeline, NodeID: "job", PeakCores: 1.5}}
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
					if w.Code != 200 || got.PeakCores != 1.5 {
						t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
					}
				}
				if f.reads != 1 || f.pipeline != pipeline || f.job != "job" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("read = %+v; headers=%v", f, w.Header())
				}
				if class != "member" && (b.bindingRule != capability.BindPipelineOfBoundRun || b.bindingTarget.Pipeline != pipeline || b.bindingTarget.NodeID != "job") {
					t.Fatalf("binding=%s target=%+v", b.bindingRule, b.bindingTarget)
				}
			})
		}
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
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
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
					p.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Repository: "acme/repo", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "123", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
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
				b.snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *p.Capability.Binding.Expansion, ExpiresAt: expires}
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
			if f.reads != reads || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("reads=%d body=%s", f.reads, w.Body.String())
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
				bindTenantPort[controller.TenantHandle](s, func(context.Context, store.Team) (controller.TenantHandle, error) { return nil, nil })
			case "store error":
				bindTenantPort[controller.TenantHandle](s, func(context.Context, store.Team) (controller.TenantHandle, error) {
					return nil, errors.New("private resolver")
				})
				code = apierr.Internal
			case "no policy":
				s.WithAuthorization(routepolicy.Authorization{})
			}
			w := sizingRequest(s.Handler(), b, "GET", "/api/v1/pipelines/build/sizing", `{}`)
			assertEngineError(t, w, apierr.Status(code), code)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Body.String())
			}
		})
	}
	b, f := newCredentialBoundary(), &sizingStore{}
	h := sizingServer(t, b, f)
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		w := sizingRequest(h, b, method, "/api/v1/pipelines/build/sizing", `{}`)
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		if w.Header().Get("Allow") != "GET" {
			t.Fatalf("Allow=%q want GET", w.Header().Get("Allow"))
		}
	}
	for _, leaf := range []string{"/pin", "/observations", "/contention", "/waits"} {
		if w := sizingRequest(h, b, "POST", "/api/v1/pipelines/build/sizing"+leaf, `{}`); w.Code != 404 {
			t.Fatalf("unregistered sizing write %s HTTP %d %s", leaf, w.Code, w.Body.String())
		}
	}
	if f.reads != 0 {
		t.Fatalf("store reached: %+v", f)
	}
}

func (*sizingStore) Team() store.Team { return "team" }
