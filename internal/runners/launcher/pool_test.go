package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestPoolClaimsLaunchFreshPodsWithoutThePoolCredential(t *testing.T) {
	base, kube, boundary, _ := fixture()
	st, root := poolControllerStore(t)
	engine := controller.NewEngineController(st, controller.EngineController{RunnerPolicy: poolRunnerPolicy{}})
	handler := controller.New(st, nil).WithEngineController(engine).Handler()
	transport := poolControllerTransport{handler}
	l, err := NewLauncher(t.Context(), RuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: base.Config,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}},
		Bearer: func(context.Context) (string, error) { return root, nil },
		HTTP:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if launched, err := l.LaunchOne(t.Context()); err != nil || !launched {
			t.Fatalf("pool launch: %v %v", launched, err)
		}
	}
	jobs, err := kube.BatchV1().Jobs(base.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 2 || jobs.Items[0].Name == jobs.Items[1].Name {
		t.Fatalf("claims reused a Job: %v %v", jobs, err)
	}
	for _, job := range jobs.Items {
		secret, err := kube.CoreV1().Secrets(job.Namespace).Get(t.Context(), job.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range secret.Data {
			if strings.Contains(string(body), root) {
				t.Fatal("job bootstrap disclosed reusable pool authority")
			}
		}
		pod := job.Spec.Template.Spec
		if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.ServiceAccountName != ServiceAccount {
			t.Fatal("job can reach the launcher's Kubernetes identity")
		}
		for _, volume := range pod.Volumes {
			if volume.EmptyDir == nil && (volume.Secret == nil || volume.Secret.SecretName != job.Name) {
				t.Fatalf("job mounted reusable state or pool authority: %+v", volume)
			}
		}
	}
}

type poolControllerTransport struct{ handler http.Handler }

func (p poolControllerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

type poolRunnerPolicy struct{}

func (poolRunnerPolicy) CheckRunner(context.Context, string) error { return nil }
func (poolRunnerPolicy) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{Labels: []string{"sparkwing-4cpu"}}, nil
}

func poolControllerStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "pool.db"), store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{ClaimProfiles: poolRunnerPolicy{}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := teststore.Exec(t.Context(), st, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := "swk_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	digest := sha256.Sum256([]byte(raw))
	root := capability.Capability{Prefix: "pool-root", Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "pool-root"}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: []string{"deploy"}, Trust: capability.TrustVerifiedDefault, Repositories: []string{"github.com/team/repo"}, IssuedAt: time.Unix(0, 1)}
	origin := store.CapabilityOrigin{Kind: capability.ProvisionMachine, RegistrationID: "runner:team:pool-root"}
	exec(`INSERT INTO teams(name,created_at,updated_at,next_run_number) VALUES ('team',1,1,2)`)
	exec(`INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,'pool-root','team','machine','pool-root','controller',?,'delegate',1,?,'none',?,?,1,'fixture')`, hex.EncodeToString(digest[:]), encode(root.Grants), encode(store.CapabilityRecord{Version: 1, Value: root, Origin: &origin}), encode(root.ReservedPools), encode(root.Repositories))
	unit := wireAward(capability.UnitNode)
	request := unit.Request.Request
	request.Git.Repository.Key = "gh:1"
	request.Git.Repository.Display = "https://github.com/team/repo"
	request.Version, request.Team, request.Pipeline, request.Mode, request.Dispatch = 1, "team", "ci", runspec.IntakeExecute, runspec.DispatchHost
	request.Source.Ref = request.Git.SHA
	app := runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, Ref: "refs/heads/main", SHA: request.Git.SHA, DefaultBranch: "main", DefaultBranchPush: true, DeliveryKey: "pool-delivery", VerifiedAt: time.Unix(1, 0)}
	provenance, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	request.Git.Ref = app.Ref
	request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: app}
	sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: "run", AdmittedAt: time.Unix(1, 0), Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	plan := encode(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job-a"}, {ID: "job-b"}}})
	spec, err := runspec.SealSpec(runspec.SealInput{Request: sealed, Plan: plan, Now: time.Unix(1, 0), Resolved: runspec.Resolved{SecretArgs: []string{}, Engine: *unit.Engine, Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,spec_json,spec_hash,sealed_at,plan_json,plan_hash,log_retention_class,log_retention_days) VALUES ('team','run',1,'ci','running',?,?,1,?,'host','execute','account',1,?,?,1,?,?,'r30',30)`, encode(sealed), sealed.Hash, request.Git.Repository.Key, encode(spec), spec.Hash, plan, spec.PlanHash)
	exec(`UPDATE runs SET repo_display=?,repo_identity='github.com/team/repo' WHERE team='team' AND id='run'`, request.Git.Repository.Display)
	exec(`INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('team','deploy','deploy','gh:1','fixture',1)`)
	exec(`INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('team','run','trigger',1,1,?,?,?)`, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	for _, id := range []string{"job-a", "job-b"} {
		exec(`INSERT INTO nodes(team,run_id,node_id,status,ready_at,queue_started_at,spec_json,execution_json) VALUES ('team','run',?,'pending',1,1,?,?)`, id, encode(nodeproto.JobSpec{ID: id}), []byte(`{"requires":{"set":true,"value":["pool=deploy"]}}`))
	}
	return st, raw
}
