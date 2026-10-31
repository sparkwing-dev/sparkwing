package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestCloudAwardResourcesFromControllerThroughLauncher(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			e, tenant := cloudResourceStore(t)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			labels := func(label string) runspec.Setting[[]string] {
				return runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{label}}}
			}
			run.Request.Request.Dispatch = runspec.DispatchController
			run.Request.Request.Git.SHA = strings.Repeat("a", 40)
			run.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://github.com/team/repo", PipelineDir: ".sparkwing"}
			run.Request.Request.Options.Requires = labels("sparkwing-2cpu")
			request, err := json.Marshal(run.Request)
			if err != nil {
				t.Fatal(err)
			}
			cloudResourceSQL(t, e.Store, `UPDATE runs SET dispatch='controller',request_json=?`, request)
			cloudResourceSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
			if kind != capability.UnitPlan {
				sealed := runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{Engine: engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("b", 64)}, Settings: runspec.ExecutionSettings{Options: runspec.Options{Requires: labels("sparkwing-2cpu")}}, Guards: runspec.Guards{Passed: true}}}
				spec, err := json.Marshal(sealed)
				if err != nil {
					t.Fatal(err)
				}
				execution, err := json.Marshal(runspec.JobExecutionSettings{Requires: labels("sparkwing-2cpu")})
				if err != nil {
					t.Fatal(err)
				}
				cloudResourceSQL(t, e.Store, `UPDATE runs SET sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan'`, spec, []byte(`{"jobs":[{"id":"job"}]}`))
				if err := teststore.SeedTriggerClaim(e.Store, t.Context(), "a", "run", runstate.Actor{CapabilityPrefix: "owner", NodeFence: runspec.NodeFence{RunID: "run", Holder: "owner", Generation: 1}}, time.Now().Add(time.Hour), nil); err != nil {
					t.Fatal(err)
				}
				cloudResourceSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), execution)
				if kind == capability.UnitEval {
					cloudResourceSQL(t, e.Store, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
					cloudResourceSQL(t, e.Store, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',7,'pending')`)
				}
			}
			raw := cloudResourceRoot(t, e)
			e.RunnerPolicy = cloudResourcePolicy(func(context.Context, string) error { return nil })
			handler := controller.New(e.Store, nil).WithEngineController(e.EngineController).Handler()
			transport := &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w.Result(), nil
			})}
			wire, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: transport})
			if err != nil {
				t.Fatal(err)
			}
			control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{kind}})
			if err != nil {
				t.Fatal(err)
			}
			award, err := control.Claim(t.Context(), "cloud", nil)
			if err != nil {
				t.Fatal(err)
			}
			if award == nil || award.CPUCores != 2 || award.Memory != store.CPUClassMemoryBytes(2) {
				t.Fatalf("award=%+v; want the committed two-core allocation", award)
			}
			states, err := control.Sync(t.Context(), []CloudJobObservation{{Binding: award.Binding, JobName: jobName(award.Binding), Capacity: &CloudCapacityObservation{PodUID: "pod-uid"}, Unheld: true}})
			if err != nil || len(states) != 1 || states[0].Delete || states[0].Resources == nil || states[0].Resources.Cores != award.CPUCores || states[0].Resources.MemoryBytes != award.Memory {
				t.Fatalf("sync did not return the committed allocation for a capacity request: %+v %v", states, err)
			}
			bootstrap, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", HTTP: transport, Bearer: func(context.Context) (string, error) { return string(award.Bootstrap), nil }})
			if err != nil {
				t.Fatal(err)
			}
			exchanged, err := bootstrap.Exchange(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if exchanged.Claim.CloudResources == nil || exchanged.Claim.CloudResources.Cores != 2 || exchanged.Claim.CloudResources.MemoryBytes != store.CPUClassMemoryBytes(2) {
				t.Fatalf("exchange changed the committed allocation: %+v", exchanged.Claim.CloudResources)
			}
			if exchanged.Claim.Execution == nil || !slices.Equal(exchanged.Claim.Execution.Requires.Value, []string{"sparkwing-2cpu"}) {
				t.Fatalf("exchange changed execution resources: %+v", exchanged.Claim.Execution)
			}
		})
	}
}

type cloudResourceTransport func(*http.Request) (*http.Response, error)

func (f cloudResourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cloudResourceProfile func(context.Context, capability.Principal) (match.Profile, error)

func (f cloudResourceProfile) ResolveClaimProfile(ctx context.Context, p capability.Principal) (match.Profile, error) {
	return f(ctx, p)
}

type cloudResourcePolicy func(context.Context, string) error

func (f cloudResourcePolicy) CheckRunner(ctx context.Context, v string) error { return f(ctx, v) }
func cloudResourceSQL(t *testing.T, s *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := teststore.Exec(t.Context(), s, q, args...); err != nil {
		t.Fatal(err)
	}
}

type cloudResourceFixture struct {
	*controller.EngineController
	Store *store.Store
}

func cloudResourceStore(t *testing.T) (*cloudResourceFixture, *store.Tenant) {
	t.Helper()
	now := time.Now().UTC()
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(func() time.Time { return now }), store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{ClaimProfiles: cloudResourceProfile(func(context.Context, capability.Principal) (match.Profile, error) {
			return match.Profile{}, nil
		})}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	cloudResourceSQL(t, s, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	cloudResourceSQL(t, s, `INSERT INTO credit_grants(reference,id,team,kind,amount_micro,created_at) VALUES ('fund','fund','a','paid',1000000000,1)`)
	provenance, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), "", "", "ci")
	if err != nil {
		t.Fatal(err)
	}
	// safety: repository-scoped launchers require the proof intake seals after fetching source.
	provenance, err = provenance.FetchedFrom(1, "team/repo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(runspec.SealedRequest{Version: 1, RunID: "run", Hash: "request", Provenance: provenance, Request: runspec.Request{Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	cloudResourceSQL(t, s, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'request',1,'gh:1','controller','execute','account',?,'r30',30)`, request, time.Now().UnixNano())
	cloudResourceSQL(t, s, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a','run','plan',1,1,?,?,?)`, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	return &cloudResourceFixture{EngineController: controller.NewEngineController(s, controller.EngineController{}), Store: s}, tenant
}

func cloudResourceRoot(t *testing.T, e *cloudResourceFixture) string {
	t.Helper()
	secret := sha256.Sum256([]byte("launcher"))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(raw))
	c := capability.Capability{Prefix: "launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: []string{}, Repositories: []string{"gh:1"}, IssuedAt: time.Unix(0, 1).UTC()}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		t.Fatal(err)
	}
	cloudResourceSQL(t, e.Store, `INSERT INTO capabilities(digest,prefix,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,'launcher','service','launcher','controller',?,'delegate',1,?,'none',?,?,1,'launcher')`, hex.EncodeToString(digest[:]), grants, envelope, []byte(`[]`), []byte(`["gh:1"]`))
	return raw
}

func TestCloudCapacityFailureThroughController(t *testing.T) {
	e, tenant := cloudResourceStore(t)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git.SHA = strings.Repeat("a", 40)
	run.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://github.com/team/repo", PipelineDir: ".sparkwing"}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	cloudResourceSQL(t, e.Store, `UPDATE runs SET request_json=?`, body)
	raw := cloudResourceRoot(t, e)
	e.RunnerPolicy = cloudResourcePolicy(func(context.Context, string) error { return nil })
	handler := controller.New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	transport := &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	wire, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: transport})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil {
		t.Fatal(err)
	}
	l, kube, boundary, _ := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	l.Control = control
	l.Boundary = capacityBoundary{CloudBoundary: boundary, pools: &KubernetesBoundary{Namespace: "cloud", Karpenter: poolClient()}}
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := e.RunDetail(t.Context(), capability.Authorized{Target: capability.Target{Team: "a", RunID: "run"}})
	if err != nil || detail.Status != "failed" || detail.Reason != capability.CloudCapacityUnconfigured || !strings.Contains(detail.Failure, capability.CloudCapacityUnconfiguredMessage) || !strings.Contains(detail.Failure, "Support reference: run") {
		t.Fatalf("customer status: %+v %v", detail, err)
	}
	if len(detail.Notices) != 0 || len(capacityPods(t, kube)) != 0 {
		t.Fatal("failure retained transient demand")
	}
}
