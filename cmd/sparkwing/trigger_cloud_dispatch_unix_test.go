//go:build unix

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	authenticationv1 "k8s.io/api/authentication/v1"

	batchv1 "k8s.io/api/batch/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	ktesting "k8s.io/client-go/testing"
)

type cloudDispatch struct {
	state    *store.Store
	endpoint string
	request  controllerwire.CreateRunRequest
	member   controllerwire.Controller
	launcher string
	open     func(string, bool) controllerwire.Controller
}

func cloudDispatchTrigger(t *testing.T, args ...string) cloudDispatch {
	t.Helper()
	return cloudDispatchSetup(t, true, args...)
}

func cloudDispatchSetup(t *testing.T, funded bool, args ...string) cloudDispatch {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n", 0o600)
	writeSDKFixture(t, filepath.Join(repo, ".sparkwing"), `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{ sw.Base }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "verify", func(context.Context) error { return nil })
 return nil
}
func main() { sw.Register("Deploy", func() sw.Pipeline[sw.NoInputs] { return pipeline{} }); sw.Main() }
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "cloud source")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://git.example/acme/app.git")
	writeSnapshotFile(t, repo, "edited.txt", "working tree only\n", 0o600)
	t.Chdir(repo)

	api := httptest.NewUnstartedServer(nil)
	t.Cleanup(api.Close)
	endpoint := "http://" + api.Listener.Addr().String()
	now := time.Now().UTC()
	state, server, member := burstIntakeDeploymentController(t, endpoint, deploymenttest.Cloud(t, endpoint), teststore.WithClock(func() time.Time { return now }))
	api.Config.Handler = server.Handler()
	api.Start()
	if err := os.WriteFile(os.Getenv("SPARKWING_CONFIG"), []byte("profiles:\n  cloud: { controller: { url: "+endpoint+", token: "+member+" } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(raw string, launcherRoute bool) controllerwire.Controller {
		t.Helper()
		c, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, Launcher: launcherRoute, Bearer: func(context.Context) (string, error) { return raw, nil }})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	seed := sha256.Sum256([]byte("cloud-dispatch-bootstrap"))
	admin := "swk_" + base64.RawURLEncoding.EncodeToString(seed[:])
	if _, err := controller.EnsureBootstrapAdminToken(t.Context(), state.AsOperator(), admin); err != nil {
		t.Fatal(err)
	}
	tokens, err := client.NewTokenManager(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return admin, nil }}, true)
	if err != nil {
		t.Fatal(err)
	}
	service, err := tokens.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: "service", Principal: "launcher", Scopes: []capability.Grant{capability.ClaimsLaunch}})
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := state.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if funded {
		if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "cloud-dispatch", CreatedBy: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	flags, err := parseTriggerFlagsArgs(append([]string{"deploy-prod", "--profile=cloud", "--working-tree"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareNativeTrigger(t.Context(), flags)
	if err != nil {
		t.Fatalf("working-tree trigger preparation: %v", err)
	}
	if !request.Request.Source.BundleDigest.Set || request.Request.Dispatch != "" {
		t.Fatalf("CLI request changed shape: bundle=%v dispatch=%s", request.Request.Source.BundleDigest.Set, request.Request.Dispatch)
	}
	return cloudDispatch{state: state, endpoint: endpoint, request: request, member: open(member, false), launcher: service.Token, open: open}
}

func TestCloudWorkingTreeTriggerRunsPlanEvalAndJobsOnTheLauncher(t *testing.T) {
	f := cloudDispatchTrigger(t)
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if cloudDispatchRequest(t, f, created.RunID).Request.Dispatch != runspec.DispatchController || created.HereBearer != "" {
		t.Fatalf("Cloud intake sealed dispatch=%s here=%v", cloudDispatchRequest(t, f, created.RunID).Request.Dispatch, created.HereBearer != "")
	}
	cloud := newCloudDispatchLauncher(t, f)
	planned := cloud.launch(t, capability.UnitPlan, "")
	engineID := engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3", InputsDigest: strings.Repeat("c", 64)}, ManifestDigest: strings.Repeat("b", 64)}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "source", Steps: []nodeproto.StepSpec{{ID: "run"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"expand"}}}}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: cloudDispatchRequest(t, f, created.RunID), Declarations: declarations, Plan: doc, Context: settings.Context{}})
	if err != nil {
		t.Fatal(err)
	}
	resolution := runspec.Resolved{Entrypoint: "Deploy", Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}, Engine: engineID, Env: cloudDispatchRequest(t, f, created.RunID).Request.Env}
	if _, err := f.open(planned.Capability, false).AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: resolution}); err != nil {
		t.Fatalf("plan pod acceptance: %v", err)
	}
	cloud.finish(t)
	cloud.complete(t, cloud.launch(t, capability.UnitNode, "source"))
	evaluated := cloud.launch(t, capability.UnitEval, "source")
	expansion, err := json.Marshal(controllerwire.ExpansionSubmission{SourceGeneration: evaluated.SourceGeneration, Registration: runspec.SecretRegistration{Version: 1}, Body: json.RawMessage(`{"jobs":[{"id":"generated","groups":["expand"],"steps":[{"id":"run"}]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var submission controllerwire.ExpansionSubmission
	if err := json.Unmarshal(expansion, &submission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(evaluated.Capability, false).AcceptExpansion(t.Context(), created.RunID, "source", submission); err != nil {
		t.Fatalf("eval pod expansion: %v", err)
	}
	cloud.finish(t)
	cloud.complete(t, cloud.launch(t, capability.UnitNode, "generated"))
	detail, err := f.member.ReadRun(t.Context(), created.RunID)
	if err != nil || detail.Status != string(runstate.RunSuccess) || len(detail.Jobs) != 2 {
		t.Fatalf("launcher-run outcome: %+v %v", detail, err)
	}
	if want := []capability.UnitKind{capability.UnitPlan, capability.UnitNode, capability.UnitEval, capability.UnitNode}; strings.Join(unitKinds(cloud.claimed), ",") != strings.Join(unitKinds(want), ",") {
		t.Fatalf("launcher claimed %v, want %v", cloud.claimed, want)
	}
}

func TestCloudTriggerWithRunnerSelectorStaysOnTheRunner(t *testing.T) {
	f := cloudDispatchTrigger(t, "--runner=builder")
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if cloudDispatchRequest(t, f, created.RunID).Request.Dispatch != runspec.DispatchHost {
		t.Fatalf("runner-selected Cloud trigger sealed dispatch=%s", cloudDispatchRequest(t, f, created.RunID).Request.Dispatch)
	}
	poll := controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}}
	if empty, err := f.open(f.launcher, true).Claim(t.Context(), poll); err != nil || empty.Claim != nil {
		t.Fatalf("launcher took a runner-selected run: %+v %v", empty.Claim, err)
	}
	raw := cloudDispatchMachine(t, f, "builder")
	owner, err := f.open(raw, false).Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitTrigger}, OS: "linux", Arch: "amd64"})
	if err != nil || owner.Claim == nil || owner.Claim.Unit != capability.UnitTrigger || owner.Claim.RunID != created.RunID {
		t.Fatalf("selected runner did not take the trigger: %+v %v", owner.Claim, err)
	}
}

func TestCloudTriggerNoticesWorkNobodyHasClaimed(t *testing.T) {
	for _, runner := range []string{"", "--runner=builder"} {
		t.Run(map[string]string{"": "launcher", "--runner=builder": "runner"}[runner], func(t *testing.T) {
			var args []string
			if runner != "" {
				args = append(args, runner)
			}
			f := cloudDispatchTrigger(t, args...)
			created, err := f.member.CreateRun(t.Context(), f.request)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := f.member.ReadRun(t.Context(), created.RunID)
			if err != nil || len(detail.Notices) != 0 {
				t.Fatalf("fresh run already carries notices: %+v %v", detail.Notices, err)
			}
			if _, err := teststore.SQL(t, f.state).ExecContext(t.Context(), `UPDATE triggers SET available_at=available_at-? WHERE id=?`, int64(time.Minute), created.RunID); err != nil {
				t.Fatal(err)
			}
			detail, err = f.member.ReadRun(t.Context(), created.RunID)
			kind, reason := engineid.NoticeUnclaimed, "no runner or launcher has claimed the run's"
			if runner == "" {
				kind, reason = engineid.NoticeNoEligibleRunner, "no runner of the allowed tiers"
			}
			if err != nil || len(detail.Notices) != 1 || detail.Notices[0].Kind != kind || !strings.Contains(detail.Notices[0].Reason, reason) {
				t.Fatalf("unclaimed run notice: %+v %v", detail.Notices, err)
			}
		})
	}
}

func TestCloudTriggerWithoutCreditsNamesTheCreditHold(t *testing.T) {
	f := cloudDispatchSetup(t, false)
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	poll := controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}}
	if empty, err := f.open(f.launcher, true).Claim(t.Context(), poll); err != nil || empty.Claim != nil {
		t.Fatalf("launcher claimed unfunded work: %+v %v", empty.Claim, err)
	}
	if detail, err := f.member.ReadRun(t.Context(), created.RunID); err != nil || len(detail.Notices) != 0 {
		t.Fatalf("fresh unfunded run already carries notices: %+v %v", detail.Notices, err)
	}
	if _, err := teststore.SQL(t, f.state).ExecContext(t.Context(), `UPDATE triggers SET available_at=available_at-? WHERE id=?`, int64(time.Minute), created.RunID); err != nil {
		t.Fatal(err)
	}
	detail, err := f.member.ReadRun(t.Context(), created.RunID)
	if err != nil || len(detail.Notices) != 1 || detail.Notices[0].Kind != engineid.NoticeCreditHold || !strings.HasPrefix(detail.Notices[0].Reason, "out of credits:") {
		t.Fatalf("unfunded run notice: %+v %v", detail.Notices, err)
	}
	tenant, err := f.state.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "late-funding", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if detail, err = f.member.ReadRun(t.Context(), created.RunID); err != nil || len(detail.Notices) != 1 || detail.Notices[0].Kind != engineid.NoticeUnclaimed {
		t.Fatalf("funded but unclaimed run notice: %+v %v", detail.Notices, err)
	}
	if claimed, err := f.open(f.launcher, true).Claim(t.Context(), poll); err != nil || claimed.Claim == nil || claimed.Claim.RunID != created.RunID {
		t.Fatalf("launcher did not claim funded work: %+v %v", claimed.Claim, err)
	}
	if detail, err = f.member.ReadRun(t.Context(), created.RunID); err != nil || len(detail.Notices) != 0 {
		t.Fatalf("claimed run kept a notice: %+v %v", detail.Notices, err)
	}
}

func cloudDispatchMachine(t *testing.T, f cloudDispatch, name string) string {
	t.Helper()
	var prefix string
	if err := teststore.SQL(t, f.state).QueryRowContext(t.Context(), `SELECT prefix FROM capabilities WHERE subject_kind='operator' AND parent_id IS NULL`).Scan(&prefix); err != nil {
		t.Fatal(err)
	}
	issued, err := f.state.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, RegistrationID: "runner:default:" + name, Authority: &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Repositories: []string{"git.example/acme/*"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(issued.Raw, false).Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitNode}, OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

type cloudDispatchLauncher struct {
	f       cloudDispatch
	kube    *fake.Clientset
	runtime *launcher.Launcher
	claimed []capability.UnitKind
}

func TestCloudDispatchLauncherCanClaimWithConfiguredAdmission(t *testing.T) {
	l := newCloudDispatchLauncher(t, cloudDispatch{endpoint: "https://controller.example"})
	control := &cloudDispatchClaimProbe{}
	l.runtime.Control = control
	if launched, err := l.runtime.LaunchOne(t.Context()); err != nil || launched || control.claims != 1 {
		t.Fatalf("launcher admission: launched=%v claims=%d error=%v", launched, control.claims, err)
	}
}

type cloudDispatchClaimProbe struct {
	launcher.CloudControl
	claims int
}

func (c *cloudDispatchClaimProbe) Claim(context.Context, string, []string) (*launcher.CloudJobAward, error) {
	c.claims++
	return nil, nil
}

func newCloudDispatchLauncher(t *testing.T, f cloudDispatch) *cloudDispatchLauncher {
	t.Helper()
	l := &cloudDispatchLauncher{f: f, kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}})}
	created := 0
	l.kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		created++
		switch object := action.(ktesting.CreateAction).GetObject().(type) {
		case *batchv1.Job:
			object.UID = types.UID("job-" + string(rune('a'+created)))
		case *corev1.Secret:
			object.UID = types.UID("secret-" + string(rune('a'+created)))
		}
		return false, nil, nil
	})
	l.kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		objects, err := l.kube.Tracker().List(batchv1.SchemeGroupVersion.WithResource("jobs"), batchv1.SchemeGroupVersion.WithKind("Job"), action.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		list := &corev1.PodList{}
		for _, job := range objects.(*batchv1.JobList).Items {
			if job.DeletionTimestamp != nil || job.Spec.Suspend != nil && *job.Spec.Suspend {
				continue
			}
			pod := corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
			pod.Name, pod.Namespace = job.Name+"-pod", job.Namespace
			owner := true
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &owner}}
			list.Items = append(list.Items, pod)
		}
		return true, list, nil
	})
	image := "runner@sha256:" + strings.Repeat("a", 64)
	jobs := launcher.CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: f.endpoint, CPUCeiling: 64, MemoryCeiling: 64 << 30}
	identity := "system:serviceaccount:cloud:launcher"
	l.kube.PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: identity}}}, nil
	})
	policies, bindings := launcher.AdmissionPolicies(launcher.AdmissionConfig{Namespace: jobs.Namespace, Launcher: identity, RunnerImage: jobs.RunnerImage, DockerImage: jobs.DockerImage})
	for i := range policies {
		if err := l.kube.Tracker().Add(policies[i]); err != nil {
			t.Fatal(err)
		}
		if err := l.kube.Tracker().Add(bindings[i]); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{
		Kube: l.kube, Boundary: cloudDispatchBoundary{}, Jobs: jobs,
		Demand: controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}},
		Bearer: func(context.Context) (string, error) { return f.launcher, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	l.runtime = runtime
	return l
}

func (l *cloudDispatchLauncher) launch(t *testing.T, kind capability.UnitKind, job string) controllerwire.ClaimedUnit {
	t.Helper()
	if launched, err := l.runtime.LaunchOne(t.Context()); err != nil || !launched {
		t.Fatalf("launcher %s claim: launched=%v %v", kind, launched, err)
	}
	jobs, err := l.kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("published Cloud Jobs: %d %v", len(jobs.Items), err)
	}
	secret, err := l.kube.CoreV1().Secrets("cloud").Get(t.Context(), jobs.Items[0].Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for name, body := range secret.Data {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	award, err := launcher.ReadBootstrapAward(filepath.Join(directory, "bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	var unit controllerwire.ClaimedUnit
	host := launcher.BootstrapHost{ControllerURL: l.f.endpoint, Host: cloudDispatchHost(func(_ context.Context, exchanged controllerwire.ClaimedUnit) error { unit = exchanged; return nil })}
	if err := host.Run(t.Context(), award); err != nil {
		t.Fatalf("Cloud %s bootstrap: %v", kind, err)
	}
	if unit.Unit != kind || unit.JobID != job || unit.Delivery != capability.DeliveryDirect {
		t.Fatalf("launcher awarded %s %q, want %s %q", unit.Unit, unit.JobID, kind, job)
	}
	l.claimed = append(l.claimed, unit.Unit)
	return unit
}

func (l *cloudDispatchLauncher) complete(t *testing.T, unit controllerwire.ClaimedUnit) {
	t.Helper()
	work := l.f.open(unit.Capability, false)
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: l.f.endpoint, Bearer: func(context.Context) (string, error) { return unit.Capability, nil }}, unit.RunID, unit.JobID)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if result, err := host.MayStart(t.Context(), runstate.CapacityBinding{RunID: unit.RunID, NodeID: unit.JobID, Generation: unit.Binding.Generation}, runstate.Actor{}); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("Cloud %s admission: %+v: %v", unit.JobID, result, err)
	}
	started, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: &unit.Binding.Attempt})
	if err != nil {
		t.Fatalf("Cloud %s start: %v", unit.JobID, err)
	}
	if _, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
		t.Fatalf("Cloud %s report: %v", unit.JobID, err)
	}
	l.finish(t)
}

func (l *cloudDispatchLauncher) finish(t *testing.T) {
	t.Helper()
	if err := l.runtime.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := l.kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("finished unit kept its Cloud Job: %d %v", len(jobs.Items), err)
	}
}

func unitKinds(kinds []capability.UnitKind) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}

type cloudDispatchHost func(context.Context, controllerwire.ClaimedUnit) error

func (h cloudDispatchHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return h(ctx, unit)
}

type cloudDispatchBoundary struct{}

func (cloudDispatchBoundary) Reserve(_ context.Context, binding launcher.CloudJobBinding, job string) (launcher.CloudNodeReservation, error) {
	return launcher.CloudNodeReservation{NodeName: "cloud-node", NodeUID: "cloud-node-uid", Team: binding.Team, JobName: job, ReservationID: "reservation-" + job, Award: binding.LeaseRef()}, nil
}

func (cloudDispatchBoundary) Release(context.Context, launcher.CloudNodeReservation) error {
	return nil
}

func (cloudDispatchBoundary) Retire(context.Context, launcher.CloudNodeReservation) error { return nil }

func TestCloudJobReportsThroughTheNodeHostRoute(t *testing.T) {
	f := cloudDispatchTrigger(t)
	created, err := f.member.CreateRun(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	cloud := newCloudDispatchLauncher(t, f)
	planned := cloud.launch(t, capability.UnitPlan, "")
	engineID := engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3", InputsDigest: strings.Repeat("c", 64)}, ManifestDigest: strings.Repeat("b", 64)}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "greet", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	request := cloudDispatchRequest(t, f, created.RunID)
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: request, Declarations: declarations, Plan: doc, Context: settings.Context{}})
	if err != nil {
		t.Fatal(err)
	}
	resolution := runspec.Resolved{Entrypoint: "Deploy", Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}, Engine: engineID, Env: request.Request.Env}
	if _, err := f.open(planned.Capability, false).AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: resolution}); err != nil {
		t.Fatalf("plan pod acceptance: %v", err)
	}
	cloud.finish(t)
	unit := cloud.launch(t, capability.UnitNode, "greet")
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: f.endpoint, Bearer: func(context.Context) (string, error) { return unit.Capability, nil }}, unit.RunID, unit.JobID)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatalf("Cloud job principal: %v", err)
	}
	if result, err := host.MayStart(t.Context(), runstate.CapacityBinding{RunID: unit.RunID, NodeID: unit.JobID, Generation: unit.Binding.Generation}, runstate.Actor{}); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("Cloud job admission: %+v: %v", result, err)
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix}
	source := runspec.AttemptOrdinal(unit.Binding.Attempt)
	started, err := host.TransitionNode(t.Context(), ref, actor, runstate.NodeCommandPayload{To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: &source})
	if err != nil {
		t.Fatalf("Cloud job start through the node host route: %v", err)
	}
	attempted, err := host.Principal(t.Context())
	if err != nil {
		t.Fatalf("Cloud job attempt principal: %v", err)
	}
	if _, err := host.BindStorage(t.Context(), attempted); err != nil {
		t.Fatalf("Cloud job storage: %v", err)
	}
	b := attempted.Capability.Binding
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempted.Capability.Team, RunID: unit.RunID, Attempt: &runspec.AttemptIdentity{Team: attempted.Capability.Team, RunID: unit.RunID, NodeID: unit.JobID, Generation: b.Generation, Attempt: b.Attempt}}
	uploader, err := host.LogUploader(t.Context(), attempted, subject)
	if err != nil {
		t.Fatalf("Cloud job log uploader: %v", err)
	}
	sink, err := logseg.NewSink(t.Context(), uploader, logseg.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append([]byte(`{"msg":"hello from the Cloud step"}`+"\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
		t.Fatalf("Cloud job log commit: %v", err)
	}
	attempt := started.Attempt
	if _, err := host.TransitionNode(t.Context(), ref, actor, runstate.NodeCommandPayload{To: runstate.NodeFailed, Cause: runstate.CauseReport, SourceAttempt: &attempt, Extra: runstate.NodeCommandExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, MaskedError: "step failed"}}}); err != nil {
		t.Fatalf("Cloud job report through the node host route: %v", err)
	}
	cloud.finish(t)
	detail, err := f.member.ReadRun(t.Context(), created.RunID)
	if err != nil || detail.Status != string(runstate.RunFailed) {
		t.Fatalf("Cloud job outcome: %+v %v", detail, err)
	}
	page, err := f.member.(controllerwire.LogReader).ReadLogs(t.Context(), created.RunID, controllerwire.LogQuery{Job: "greet"})
	if err != nil {
		t.Fatalf("failed Cloud job logs: %v", err)
	}
	var lines []string
	for _, record := range page.Records {
		lines = append(lines, record.Line)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "hello from the Cloud step") {
		t.Fatalf("failed Cloud job step output is missing from its run logs: %+v", page.Records)
	}
}
