package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

const featureVersion = "commit-feature-e2e"

type featureController struct {
	t                               *testing.T
	ctx                             context.Context
	url, member, second, fast, slow string
	seededRuns                      []string
	profileReady                    bool
	cli                             string
	env                             []string
	http                            *http.Client
	control                         controllerwire.Controller
}

func TestFeatureE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real controller process and observes real placement deadlines")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	revision := testgit.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "HEAD")
	if output, err := revision.Output(); err == nil {
		t.Logf("controller/CLI source revision=%s", strings.TrimSpace(string(output)))
	}
	bin := filepath.Join(t.TempDir(), "sparkwing-controller")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-p", "1", "-ldflags", "-X=main.Version="+featureVersion, "-o", bin, "./cmd/sparkwing-controller")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=auto", "GOMAXPROCS=2")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controller: %v\n%s", err, output)
	}
	cli := filepath.Join(t.TempDir(), "sparkwing")
	if runtime.GOOS == "windows" {
		cli += ".exe"
	}
	build = exec.CommandContext(t.Context(), "go", "build", "-p", "1", "-o", cli, "./cmd/sparkwing")
	build.Dir, build.Env = root, append(os.Environ(), "GOWORK=auto", "GOMAXPROCS=2")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	for _, scenario := range []struct {
		name string
		run  func(*featureController)
	}{
		{"placement", testFeaturePlacement}, {"retry_failed", testFeatureRetry}, {"child_cancellation", testFeatureChildren},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFeatureController(t, bin, scenario.name)
			f.cli = cli
			scenario.run(f)
		})
	}
}

func newFeatureController(t *testing.T, bin, scenario string) *featureController {
	t.Helper()
	reusable := scenario != "child_cancellation"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	epoch, err := credentials.LoadOrCreateEpoch(state)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(state, "state.db"), store.WithCredentialEpoch(epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	teststore.RegisterSQLite(t, s, filepath.Join(state, "state.db"))
	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	f := &featureController{t: t, ctx: ctx, http: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
	for name, dest := range map[string]*string{"actor-one": &f.member, "actor-two": &f.second} {
		teststore.Console(t, s, store.DefaultTeam, name)
		if _, err := teststore.Exec(ctx, s, "UPDATE sessions SET credential_epoch=?", epoch); err != nil {
			t.Fatal(err)
		}
		raw, _, err := teststore.ProvisionCredential(ctx, s, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: name}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl}})
		if err != nil {
			t.Fatalf("credential %s: %v", name, err)
		}
		*dest = raw
	}
	if _, err := teststore.Exec(ctx, s, "UPDATE teams SET high_trust=1 WHERE name=?", store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	issuer := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: fmt.Sprintf("%x", sha256.Sum256([]byte("fixture-session:default:actor-one")))}
	for name, dest := range map[string]*string{"fast": &f.fast, "slow": &f.slow} {
		spec := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &issuer, Team: string(store.DefaultTeam), RegistrationID: "runner:default:" + name, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}}
		spec.AllowedLabels = []string{"lane=" + name, "mid-hold"}
		if name == "slow" {
			spec.AllowedLabels = append(spec.AllowedLabels, "lane=late", "keep-parent")
		}
		var issued capability.Issued
		if reusable {
			spec.Personal = false
			spec.Repositories = []string{"github.com/feature/fixture"}
			raw, _, issueErr := teststore.ProvisionCredential(ctx, s, spec)
			err = issueErr
			issued.Raw = raw
		} else {
			issued, err = s.AsOperator().ProvisionCapability(ctx, spec)
		}
		if err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
		*dest = issued.Raw
	}
	if reusable {
		if scenario == "placement" {
			for i, hold := range []time.Duration{3 * time.Second, time.Second} {
				last := "lane=slow"
				if i == 1 {
					last = "lane=late"
				}
				placement := &runspec.RepositoryPlacement{Revision: strings.Repeat("a", 40), Prefer: []runspec.RepositoryTier{{Target: runspec.PlacementTeam, Labels: []string{"lane=fast"}, Hold: hold}, {Target: runspec.PlacementTeam, Labels: []string{last}}}}
				f.seededRuns = append(f.seededRuns, seedFeatureSource(t, s, fmt.Sprintf("placement-%d", i+1), int64(i+1), placement))
			}
		} else {
			f.seededRuns = []string{seedFeatureSource(t, s, "feature-source", 1, nil)}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	f.url = "http://" + addr
	logPath := filepath.Join(home, "controller.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--addr", addr, "--placement-hold", "3s", "--placement-liveness", "30s", "--idle-claim-poll", "0s")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home, "SPARKWING_HOME=" + state, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_STATE_HOME=" + filepath.Join(home, "state"), "SPARKWING_CONFIG=" + filepath.Join(home, "config.yaml"), "SPARKWING_SECRETS_KEY_FILE=" + filepath.Join(home, "secrets.key"), "TMPDIR=" + os.Getenv("TMPDIR"), "GOMAXPROCS=2"}
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+systemRoot)
	}
	f.env = cmd.Env
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		// sleepcheck:external-boundary a controller subprocess must exit before its private state is removed
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("controller required forced cleanup")
		}
		_ = log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 60 {
				lines = lines[len(lines)-60:]
			}
			t.Logf("controller log tail:\n%s", strings.Join(lines, "\n"))
		}
	})
	f.control = f.client(f.member)
	f.await("controller readiness", func() bool {
		select {
		case <-done:
			t.Fatalf("controller exited: %v", waitErr)
		default:
		}
		resp, err := f.http.Get(f.url + "/api/v1/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return f
}

func (f *featureController) client(raw string) controllerwire.Controller {
	f.t.Helper()
	c, err := client.NewEngineClient(client.EngineOptions{URL: f.url, HTTP: f.http, Bearer: func(context.Context) (string, error) { return raw, nil }})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *featureController) await(what string, condition func() bool) {
	f.t.Helper()
	for !condition() {
		select {
		case <-f.ctx.Done():
			f.t.Fatalf("%s: %v", what, f.ctx.Err())
		// sleepcheck:external-boundary real controller processes and HTTP state need a paced poll
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *featureController) plan(request runspec.Request, jobs []nodewire.JobSpec) string {
	f.t.Helper()
	if request.Version == 0 {
		request.Version = 1
		request.Team = string(store.DefaultTeam)
		request.Pipeline = "feature"
		request.Origin = runspec.OriginTrigger
		request.Mode = runspec.IntakeExecute
		request.Dispatch = runspec.DispatchController
		request.Git = runspec.Git{Repository: runspec.Repository{Key: "git:git.example.test/feature"}, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40)}
		request.Source.Ref = request.Git.SHA
		request.Source.Location = runspec.SourceLocation{RemoteURL: "https://git.example.test/feature.git", PipelineDir: ".sparkwing"}
	}
	var created controllerwire.CreateRunResponse
	if len(f.seededRuns) > 0 {
		created.RunID = f.seededRuns[0]
		f.seededRuns = f.seededRuns[1:]
	} else {
		var err error
		created, err = f.control.CreateRun(f.ctx, controllerwire.CreateRunRequest{Request: request, IdempotencyKey: fmt.Sprintf("run-%d", time.Now().UnixNano())})
		if err != nil {
			f.t.Fatalf("intake: %v", err)
		}
	}
	f.accept(created.RunID, jobs)
	return created.RunID
}

func (f *featureController) accept(run string, jobs []nodewire.JobSpec) {
	f.t.Helper()
	var award *controllerwire.ClaimedUnit
	f.await("planning award", func() bool {
		resp, err := f.client(f.slow).Claim(f.ctx, controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan}, RunnerVersion: featureVersion, OS: runtime.GOOS, Arch: runtime.GOARCH})
		if err != nil {
			f.t.Fatalf("planning claim: %v", err)
		}
		award = resp.Claim
		return award != nil
	})
	if award.RunID != run {
		f.t.Fatalf("planner took run %s, want %s", award.RunID, run)
	}
	for i := range jobs {
		jobs[i].Steps = []nodewire.StepSpec{{ID: "body"}}
	}
	doc := nodewire.PlanDoc{Jobs: jobs}
	raw, err := json.Marshal(doc)
	if err != nil {
		f.t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Declarations: declarations, Request: award.Request, Plan: doc})
	if err != nil {
		f.t.Fatal(err)
	}
	defaultBranch := runspec.Optional[string]{}
	var defaultBranchSource runspec.DefaultBranchSource
	if award.Request.Request.Git.App.Set {
		defaultBranch = runspec.Optional[string]{Set: true, Value: award.Request.Request.Git.App.Value.DefaultBranch}
		defaultBranchSource = runspec.DefaultBranchApp
	}
	_, err = f.client(award.Capability).AcceptPlan(f.ctx, run, controllerwire.PlanSubmission{Plan: raw, Declarations: declarations, Resolved: runspec.Resolved{DefaultBranch: defaultBranch, DefaultBranchSource: defaultBranchSource, Engine: engineid.Identity{Version: featureVersion}, Args: map[string]string{}, SecretArgs: []string{}, Guards: runspec.Guards{Passed: true}, Settings: resolved, SecretRegistration: runspec.SecretRegistration{Version: 1}}})
	if err != nil {
		f.t.Fatalf("accept plan: %v", err)
	}
}

func (f *featureController) claim(raw, run, job string, labels ...string) *controllerwire.ClaimedUnit {
	f.t.Helper()
	slots := 2
	resp, err := f.client(raw).Claim(f.ctx, controllerwire.ClaimRequest{MaxConcurrent: 2, FreeSlots: &slots, Kinds: []capability.UnitKind{capability.UnitNode}, RunnerVersion: featureVersion, Labels: labels, OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		f.t.Fatalf("claim %s: %v", job, err)
	}
	if resp.Claim != nil && (resp.Claim.RunID != run || resp.Claim.JobID != job) {
		f.t.Fatalf("runner took %s/%s, want %s/%s", resp.Claim.RunID, resp.Claim.JobID, run, job)
	}
	return resp.Claim
}

func (f *featureController) finish(award controllerwire.ClaimedUnit, outcome string) {
	f.t.Helper()
	f.start(award)
	f.report(award, &controllerwire.JobResult{Outcome: outcome})
}

func (f *featureController) host(award controllerwire.ClaimedUnit) *client.LocalHostClient {
	f.t.Helper()
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: f.url, HTTP: f.http, Bearer: func(context.Context) (string, error) { return award.Capability, nil }}, award.RunID, award.JobID)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = host.Close() })
	return host
}

func (f *featureController) start(award controllerwire.ClaimedUnit) {
	f.t.Helper()
	host := f.host(award)
	admitted, err := host.MayStart(f.ctx, runstate.CapacityBinding{RunID: award.RunID, NodeID: award.JobID, Generation: award.Binding.Generation}, runstate.Actor{})
	if err != nil || admitted.Kind != runstate.AcquireGranted {
		f.t.Fatalf("job admission: kind=%s error=%v", admitted.Kind, err)
	}
	c := f.client(award.Capability)
	attempt := int64(0)
	_, err = c.TransitionJob(f.ctx, award.RunID, award.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: &attempt})
	if err != nil {
		f.t.Fatalf("start job: %v", err)
	}
}

func (f *featureController) report(award controllerwire.ClaimedUnit, result *controllerwire.JobResult) {
	f.t.Helper()
	attempt := int64(1)
	_, err := f.client(award.Capability).TransitionJob(f.ctx, award.RunID, award.JobID, controllerwire.JobTransitionRequest{To: result.Outcome, Cause: "report", SourceAttempt: &attempt, Result: result})
	if err != nil {
		f.t.Fatalf("report job: %v", err)
	}
}

func (f *featureController) awaitStatus(run, want string) controllerwire.RunDetail {
	f.t.Helper()
	var detail controllerwire.RunDetail
	f.await("run "+run+" status "+want, func() bool {
		var err error
		detail, err = f.control.ReadRun(f.ctx, run)
		if err != nil {
			f.t.Fatal(err)
		}
		if detail.Status != "running" && detail.Status != "pending" && detail.Status != want {
			f.t.Fatalf("run status=%s want=%s: %+v", detail.Status, want, detail)
		}
		return detail.Status == want
	})
	return detail
}
