package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type claimedTriggerSecrets struct {
	Required string `sw:"REQUIRED,required"`
	Optional string `sw:"OPTIONAL,optional"`
}

type claimedSecretPipe struct {
	sparkwing.Base
	body         *atomic.Int32
	wantOptional string
}

func (claimedSecretPipe) Secrets() any { return &claimedTriggerSecrets{} }
func (p claimedSecretPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "attest", func(ctx context.Context) error {
		p.body.Add(1)
		sec := sparkwing.PipelineSecrets[claimedTriggerSecrets](ctx)
		if sec == nil || sec.Required != "private-fixture-value" || sec.Optional != p.wantOptional {
			return errors.New("resolved secrets do not match the fixture")
		}
		return nil
	})
	return nil
}

type claimedCoercionPipe struct {
	sparkwing.Base
	fields any
	body   *atomic.Int32
}

func (p claimedCoercionPipe) Secrets() any { return p.fields }
func (p claimedCoercionPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "must-not-run", func(context.Context) error {
		p.body.Add(1)
		return nil
	})
	return nil
}

func TestClaimedTriggerSecretCoercionKeepsValuesOutOfRunErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields any
	}{
		{"required", &struct {
			Count int8 `sw:"COUNT,required"`
		}{}},
		{"optional", &struct {
			Count int8 `sw:"COUNT,optional"`
		}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body, reads atomic.Int32
			name := "claimed-secret-coercion-" + tc.name
			sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
				return claimedCoercionPipe{fields: tc.fields, body: &body}
			})
			rig := newTriggerWorkerRig(t)
			trig := rig.claim(t, store.Trigger{ID: "coercion-" + tc.name, Pipeline: name, TriggerSource: "manual"})
			upstream, _ := url.Parse(rig.client.BaseURL())
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			const value = "12345"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v1/secrets/") {
					proxy.ServeHTTP(w, r)
					return
				}
				reads.Add(1)
				if r.URL.Query().Get("run") != trig.ID || r.Header.Get("Authorization") != "Bearer scoped-agent" {
					t.Error("secret read lacks the claimed run or scoped credential")
					http.Error(w, "wrong-run", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(client.Secret{Value: value, Masked: true})
			}))
			defer srv.Close()
			c := client.NewWithToken(srv.URL, srv.Client(), "scoped-agent")
			orchestrator.ExecuteClaimedTrigger(t.Context(), orchestrator.WorkerOptions{Logger: rig.logger},
				orchestrator.RemoteBackends(c, nil, nil, srv.Client(), store.DefaultConcurrencyLease), c, trig)
			stored := mustRun(t, rig.st, trig.ID)
			served, err := rig.client.GetRun(t.Context(), trig.ID)
			if err != nil {
				t.Fatal(err)
			}
			if body.Load() != 0 || reads.Load() == 0 {
				t.Fatalf("body entries=%d secret reads=%d", body.Load(), reads.Load())
			}
			for _, result := range []struct {
				name string
				run  *store.Run
			}{{"stored", stored}, {"API", served}} {
				run := result.run
				if run.Status != "failed" || strings.Contains(run.Error, value) ||
					!strings.Contains(run.Error, "Count") || !strings.Contains(run.Error, "int8") {
					t.Errorf("unsafe or incomplete %s error: status=%s error=%s", result.name, run.Status, run.Error)
				}
			}
		})
	}
}

func TestClaimedTriggerResolvesRunScopedTypedSecrets(t *testing.T) {
	for _, tc := range []struct {
		name               string
		required, optional int
		want               string
	}{
		{"present", 200, 200, ""},
		{"optional-missing", 200, 404, ""},
		{"required-missing", 404, 200, "required"},
		{"claim-refused", 403, 200, "403"},
		{"controller-failure", 503, 200, "503"},
		{"optional-refused", 200, 403, "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body atomic.Int32
			name := "claimed-secret-" + tc.name
			sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
				optional := ""
				if tc.optional == 200 {
					optional = "private-fixture-value"
				}
				return claimedSecretPipe{body: &body, wantOptional: optional}
			})
			rig := newTriggerWorkerRig(t)
			trig := rig.claim(t, store.Trigger{ID: "run-" + tc.name, Pipeline: name, TriggerSource: "manual"})
			upstream, _ := url.Parse(rig.client.BaseURL())
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			var reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v1/secrets/") {
					proxy.ServeHTTP(w, r)
					return
				}
				reads.Add(1)
				if r.URL.Query().Get("run") != trig.ID || r.Header.Get("Authorization") != "Bearer scoped-agent" {
					t.Errorf("secret read lacks the claimed run or scoped credential")
					http.Error(w, "wrong-run", http.StatusForbidden)
					return
				}
				status := tc.required
				if strings.HasSuffix(r.URL.Path, "/OPTIONAL") {
					status = tc.optional
				}
				if status != 200 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "fixture refusal"})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(client.Secret{Value: "private-fixture-value", Masked: true})
			}))
			defer srv.Close()
			c := client.NewWithToken(srv.URL, srv.Client(), "scoped-agent")
			orchestrator.ExecuteClaimedTrigger(context.Background(), orchestrator.WorkerOptions{Logger: rig.logger},
				orchestrator.RemoteBackends(c, nil, nil, srv.Client(), store.DefaultConcurrencyLease), c, trig)
			run := mustRun(t, rig.st, trig.ID)
			if tc.want == "" {
				if run.Status != "success" || body.Load() != 1 {
					t.Fatalf("status=%s body=%d error=%s", run.Status, body.Load(), run.Error)
				}
			} else if run.Status != "failed" || body.Load() != 0 || !strings.Contains(run.Error, tc.want) {
				t.Fatalf("expected fail-fast %q: status=%s body=%d error=%s", tc.want, run.Status, body.Load(), run.Error)
			}
			if reads.Load() == 0 {
				t.Fatal("controller secret boundary was not reached")
			}
		})
	}
}
