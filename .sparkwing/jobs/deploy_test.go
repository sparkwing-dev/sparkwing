package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

const testRegistry = "123456789012.dkr.ecr.us-west-2.amazonaws.com"

func testDigest(seed string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(seed)))
}

func testValues(version, ctl, run string) string {
	return `# Staging Sparkwing values.
# Replace all four image references together.
controller:
  deployment: cloud
  image:
    repository: ` + testRegistry + `/sparkwing-controller
    tag: ` + version + `@` + ctl + `
  databaseSecret: { name: sparkwing-database, key: dsn }
sparkwing-runner-bundle:
  enabled: true
  controller:
    url: https://staging.sparkwing.dev
  launcher:
    enabled: true
    image:
      repository: ` + testRegistry + `/sparkwing-runner
      tag: ` + version + `@` + run + `
    tokenSecret: { name: sparkwing-launcher-token, key: token }
    jobImage:
      repository: ` + testRegistry + `/sparkwing-runner
      tag: ` + version + `
      digest: ` + run + `
    dockerImage:
      repository: docker.io/library/docker
      tag: "28-dind"
      digest: ` + testDigest("dind") + `
    tolerations:
      - key: purpose
        operator: Equal
`
}

func TestValuesPinsReadAndPin(t *testing.T) {
	oldCtl, oldRun := testDigest("old-ctl"), testDigest("old-run")
	values := testValues("v0.68.3", oldCtl, oldRun)
	pins, err := readValuesPins(values)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Controller.Digest != oldCtl || pins.Launcher.Digest != oldRun || pins.Job.Tag != "v0.68.3" || pins.Controller.Tag != "v0.68.3" {
		t.Fatalf("pins = %+v", pins)
	}
	newCtl := imagePin{Repository: testRegistry + "/sparkwing-controller", Tag: "v0.68.4-main.gabc", Digest: testDigest("new-ctl")}
	newRun := imagePin{Repository: testRegistry + "/sparkwing-runner", Tag: "v0.68.4-main.gabc", Digest: testDigest("new-run")}
	pinned, err := pinValues(values, deployPhaseController, newCtl)
	if err != nil {
		t.Fatal(err)
	}
	pins, err = readValuesPins(pinned)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Controller != newCtl || pins.Launcher.Digest != oldRun {
		t.Fatalf("controller phase pinned %+v", pins)
	}
	pinned, err = pinValues(pinned, deployPhaseLauncher, newRun)
	if err != nil {
		t.Fatal(err)
	}
	pins, err = readValuesPins(pinned)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Launcher != newRun || pins.Job != newRun {
		t.Fatalf("launcher phase pinned %+v", pins)
	}
	if want := testValues("v0.68.4-main.gabc", newCtl.Digest, newRun.Digest); pinned != want {
		t.Fatalf("pinning changed more than the image lines:\n%s", pinned)
	}
	if !strings.Contains(pinned, `tag: "28-dind"`) {
		t.Fatal("pinning rewrote the docker image")
	}

	other := newCtl
	other.Repository = "ghcr.io/sparkwing-dev/sparkwing-controller"
	if _, err := pinValues(values, deployPhaseController, other); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("pinning another repository: %v", err)
	}
	split := strings.Replace(values, "digest: "+oldRun, "digest: "+testDigest("elsewhere"), 1)
	if _, err := readValuesPins(split); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("launcher and job images that differ: %v", err)
	}
	unpinned := strings.Replace(values, "@"+oldCtl, "", 1)
	if _, err := readValuesPins(unpinned); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("an unpinned controller tag: %v", err)
	}
}

func TestDeployVersion(t *testing.T) {
	commit := strings.Repeat("ab", 20)
	for _, tc := range []struct{ newest, newestCommit, want string }{
		{"v0.68.3", commit, "v0.68.3"},
		{"v0.68.3", "other", "v0.68.4-main.gabababababab"},
		{"", "", "v0.0.1-main.gabababababab"},
	} {
		got, err := deployVersion(tc.newest, tc.newestCommit, commit)
		if err != nil || got != tc.want {
			t.Errorf("deployVersion(%q) = %q, %v; want %q", tc.newest, got, err, tc.want)
		}
	}
}

func TestDirtyLogLines(t *testing.T) {
	logs := "[pod/c] time=1 level=INFO msg=ok\n[pod/c] {\"level\":\"WARN\",\"msg\":\"slow\"}\n[pod/l] 2026 ERROR boom\n[pod/l] level=error msg=x\n"
	if got := dirtyLogLines(logs); len(got) != 3 {
		t.Fatalf("dirty lines = %q", got)
	}
}

func TestDeployPlanShape(t *testing.T) {
	host := newTestHost(t, sparkwingtest.Options{})
	deps := func(args map[string]string) map[string][]string {
		t.Helper()
		plan, err := host.Plan(t.Context(), "Deploy", args)
		if err != nil {
			t.Fatal(err)
		}
		doc := decodePipeline(t, plan)
		out := map[string][]string{}
		for _, job := range doc.Jobs {
			needs := slices.Clone(job.Needs)
			slices.Sort(needs)
			out[job.ID] = needs
		}
		return out
	}
	full := deps(nil)
	want := map[string][]string{
		"build-images":   nil,
		"staging-deploy": {"build-images"},
		"staging-gate":   {"build-images", "staging-deploy"},
		"production":     {"build-images", "staging-gate"},
	}
	if fmt.Sprint(full) != fmt.Sprint(want) {
		t.Fatalf("full plan = %v, want %v", full, want)
	}
	if got := deps(map[string]string{"stop-after": "staging"}); len(got) != 3 || got["production"] != nil {
		t.Fatalf("stop-after staging plan = %v", got)
	}
	proven := deps(map[string]string{"promote-proven": "true"})
	if _, ok := proven["staging-deploy"]; ok || fmt.Sprint(proven["production"]) != "[build-images]" {
		t.Fatalf("promote-proven plan = %v", proven)
	}
	if _, err := host.Plan(t.Context(), "Deploy", map[string]string{"stop-after": "qa"}); err == nil {
		t.Fatal("an unknown --stop-after planned")
	}
}

func TestRequireStagingPass(t *testing.T) {
	images := deployImages{Controller: imagePin{Digest: testDigest("c")}, Runner: imagePin{Digest: testDigest("r")}}
	record := func(img deployImages) json.RawMessage {
		raw, err := json.Marshal(stagingGateRecord{Images: img, RunID: "run-1"})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	gate := sparkwing.Ref[stagingGateRecord]{JobID: "staging-gate"}
	proven := sparkwing.Ref[stagingGateRecord]{JobID: "staging-gate", Pipeline: "deploy"}
	ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"staging-gate": record(images)}}).Context(t.Context())
	if err := requireStagingPass(ctx, images, gate, sparkwing.Ref[stagingGateRecord]{}); err != nil {
		t.Fatalf("same digests in this run: %v", err)
	}
	other := images
	other.Runner.Digest = testDigest("other")
	if err := requireStagingPass(ctx, other, gate, sparkwing.Ref[stagingGateRecord]{}); err == nil || !strings.Contains(err.Error(), "passed controller") {
		t.Fatalf("different digests: %v", err)
	}
	latest := newTestHost(t, sparkwingtest.Options{Latest: func(_ context.Context, pipeline, node string) (json.RawMessage, error) {
		if pipeline != "deploy" || node != "staging-gate" {
			return nil, fmt.Errorf("unexpected reference %s/%s", pipeline, node)
		}
		return record(images), nil
	}}).Context(t.Context())
	if err := requireStagingPass(latest, images, sparkwing.Ref[stagingGateRecord]{}, proven); err != nil {
		t.Fatalf("recorded proven run: %v", err)
	}
	if err := requireStagingPass(latest, other, sparkwing.Ref[stagingGateRecord]{}, proven); err == nil {
		t.Fatal("a recorded run for other digests promoted")
	}
	empty := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
	if err := requireStagingPass(empty, images, gate, sparkwing.Ref[stagingGateRecord]{}); err == nil || !strings.Contains(err.Error(), "no staging-gate record") {
		t.Fatalf("absent gate record: %v", err)
	}
}

type fakeDeployRegistry struct {
	mu        sync.Mutex
	digests   map[string]string
	revisions map[string]string
	published []PublishImagesArgs
}

func (r *fakeDeployRegistry) digest(_ context.Context, _, ref string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.digests[ref], nil
}

func (r *fakeDeployRegistry) revision(_ context.Context, _, ref string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revisions[ref], nil
}

func (r *fakeDeployRegistry) publish(_ context.Context, _ string, in PublishImagesArgs) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := os.Stat(filepath.Join(in.Dist, "SHA256SUMS")); err != nil {
		return err
	}
	r.published = append(r.published, in)
	for _, c := range []string{"sparkwing-controller", "sparkwing-runner"} {
		d := testDigest(in.Version + c)
		r.digests[in.Registry+"/"+c+":"+in.Version] = d
		r.revisions[in.Registry+"/"+c+"@"+d] = in.Revision
	}
	return nil
}

// safety: the fake cluster runs whatever the bare GitOps repository's main
// branch pins, so a test sees a rollout exactly when a push lands.
type fakeDeploy struct {
	t            *testing.T
	bare         string
	repo         string
	head         string
	old          deployImages
	registry     *fakeDeployRegistry
	mu           sync.Mutex
	said         []string
	calls        []string
	failRuns     map[string]bool
	claimsPerRun int
	dirtyLogs    bool
	clock        time.Time
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFakeDeploy(t *testing.T) *fakeDeploy {
	t.Helper()
	root := t.TempDir()
	f := &fakeDeploy{
		t: t, bare: filepath.Join(root, "gitops.git"), repo: filepath.Join(root, "sparkwing"), claimsPerRun: 1,
		failRuns: map[string]bool{}, clock: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
	if err := os.MkdirAll(filepath.Join(f.repo, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	schema := "CREATE TABLE runs (id TEXT PRIMARY KEY);\n"
	if err := os.WriteFile(filepath.Join(f.repo, deployStoreSchema), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.repo, "init", "--quiet", "-b", "main")
	gitIn(t, f.repo, "add", "-A")
	gitIn(t, f.repo, "commit", "--quiet", "-m", "source")
	f.head = gitIn(t, f.repo, "rev-parse", "HEAD")

	f.old = deployImages{
		Commit: f.head, Version: "v0.68.3", SchemaDigest: schemaBaselineDigestOf(schema),
		Controller: imagePin{testRegistry + "/sparkwing-controller", "v0.68.3", testDigest("old-ctl")},
		Runner:     imagePin{testRegistry + "/sparkwing-runner", "v0.68.3", testDigest("old-run")},
	}
	f.registry = &fakeDeployRegistry{digests: map[string]string{}, revisions: map[string]string{
		f.old.Controller.Repository + "@" + f.old.Controller.Digest: f.head,
	}}

	work := filepath.Join(root, "gitops-seed")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "init", "--quiet", "-b", "main")
	for _, dir := range []string{"sparkwing", "sparkwing-staging"} {
		values := testValues("v0.68.3", f.old.Controller.Digest, f.old.Runner.Digest)
		if err := os.MkdirAll(filepath.Join(work, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"helm-values.yaml": values, "rendered.yaml": values} {
			if err := os.WriteFile(filepath.Join(work, dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "--quiet", "-m", "seed")
	gitIn(t, root, "clone", "--quiet", "--bare", work, f.bare)
	return f
}

func (f *fakeDeploy) candidate() deployImages {
	c := f.old
	c.Version = "v0.68.4-main.g" + f.head[:12]
	c.Controller = imagePin{c.Controller.Repository, c.Version, testDigest("new-ctl")}
	c.Runner = imagePin{c.Runner.Repository, c.Version, testDigest("new-run")}
	return c
}

func (f *fakeDeploy) config() deployConfig {
	cfg := defaultDeployConfig()
	cfg.Registry = testRegistry
	cfg.GitopsURL = f.bare
	cfg.ReferenceURL = f.repo
	return cfg
}

func (f *fakeDeploy) deployer(dry bool) *deployer {
	tools := deployTools{
		run:      f.run,
		registry: f.registry,
		oidc:     func(context.Context, string) (string, error) { return "oidc-token", nil },
		wait:     func(context.Context, time.Duration) error { return nil },
		now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.clock = f.clock.Add(time.Second)
			return f.clock
		},
		say: func(_ context.Context, msg string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.said = append(f.said, msg)
		},
		mask: func(context.Context, string) error { return nil },
	}
	return &deployer{tools: tools, cfg: f.config(), dry: dry, runID: "run-deploy", repoDir: f.repo, tmp: f.t.TempDir()}
}

func (f *fakeDeploy) pinsOnMain(dir string) valuesPins {
	f.t.Helper()
	pins, err := readValuesPins(gitIn(f.t, f.bare, "show", "main:"+dir+"/helm-values.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	return pins
}

func (f *fakeDeploy) subjects() []string {
	return strings.Split(gitIn(f.t, f.bare, "log", "--format=%s", "main"), "\n")
}

func (f *fakeDeploy) run(ctx context.Context, c deployCommand) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c.String())
	f.mu.Unlock()
	arg := func(flag string) string {
		if i := slices.Index(c.Args, flag); i >= 0 && i+1 < len(c.Args) {
			return c.Args[i+1]
		}
		return ""
	}
	joined := strings.Join(c.Args, " ")
	switch name := filepath.Base(c.Name); {
	case name == "git":
		cmd := exec.CommandContext(ctx, "git", c.Args...)
		cmd.Dir = c.Dir
		cmd.Env = os.Environ()
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.Output()
		if err != nil {
			var stderr []byte
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				stderr = ee.Stderr
			}
			return string(out), fmt.Errorf("git %s: %w: %s", joined, err, stderr)
		}
		return string(out), nil
	case name == "aws":
		switch {
		case strings.HasPrefix(joined, "sts assume-role-with-web-identity"):
			if c.Env["AWS_PROFILE"] != "" || c.Env["AWS_CONFIG_FILE"] == "" {
				return "", fmt.Errorf("sts call can read the machine's AWS config: %v", c.Env)
			}
			return `{"Credentials":{"AccessKeyId":"AKIA","SecretAccessKey":"secret","SessionToken":"session"}}`, nil
		case strings.HasPrefix(joined, "ssm get-parameter"):
			if c.Env["AWS_SESSION_TOKEN"] != "session" {
				return "", fmt.Errorf("ssm read without the role session")
			}
			return "value-of-" + arg("--name") + "\n", nil
		case strings.HasPrefix(joined, "ecr get-login-password"):
			return "ecr-password\n", nil
		case strings.HasPrefix(joined, "eks update-kubeconfig"):
			return "", nil
		}
	case name == "helm":
		body, err := os.ReadFile(arg("-f"))
		return string(body), err
	case name == "bash":
		if strings.Contains(joined, "build-release-target") {
			dist := filepath.Join(c.Dir, "dist")
			if err := os.MkdirAll(dist, 0o755); err != nil {
				return "", err
			}
			for _, bin := range []string{"sparkwing-controller", "sparkwing-runner"} {
				if err := os.WriteFile(filepath.Join(dist, bin+"-linux-"+c.Env["GOARCH"]), []byte(bin), 0o755); err != nil {
					return "", err
				}
			}
		}
		return "", nil
	case name == "go":
		return "", nil
	case name == "kubectl":
		return f.kubectl(c)
	case name == "sparkwing":
		switch {
		case strings.HasPrefix(joined, "profiles add"):
			if c.Stdin == "" || c.Env["SPARKWING_HOME"] == "" {
				return "", fmt.Errorf("profile added without a token file or isolated home")
			}
			return "", nil
		case strings.HasPrefix(joined, "pipeline trigger"):
			return fmt.Sprintf(`{"run_id":"run-%s-%s"}`+"\n", c.Args[2], arg("--profile")), nil
		case strings.HasPrefix(joined, "runs status"):
			if f.failRuns[arg("--run")] {
				return "", fmt.Errorf("exit 1")
			}
			return `{"status":"success"}`, nil
		}
	}
	return "", fmt.Errorf("unexpected command %s", c)
}

func (f *fakeDeploy) kubectl(c deployCommand) (string, error) {
	joined := strings.Join(c.Args, " ")
	envDir := map[string]string{"sparkwing-staging": "sparkwing-staging", "sparkwing": "sparkwing"}
	switch {
	case strings.Contains(joined, "annotate application"), strings.Contains(joined, "rollout status"):
		return "", nil
	case strings.Contains(joined, "get application"):
		app := c.Args[len(c.Args)-3]
		if _, ok := envDir[app]; !ok {
			return "", fmt.Errorf("unknown app %s", app)
		}
		head := gitIn(f.t, f.bare, "rev-parse", "main")
		return fmt.Sprintf(`{"status":{"sync":{"status":"Synced","revision":%q},"health":{"status":"Healthy"}}}`, head), nil
	case strings.Contains(joined, "get deployment"):
		ns := c.Args[1]
		pins := f.pinsOnMain(envDir[ns])
		if strings.Contains(joined, "controller") && strings.Contains(c.Args[4], "controller") {
			return pins.Controller.ref(), nil
		}
		return pins.Launcher.ref(), nil
	case joined == "get nodes -o json":
		var items []string
		for _, run := range f.triggered("deploy-smoke") {
			items = append(items, fmt.Sprintf(`{"metadata":{"name":"node-%s","annotations":{"sparkwing.dev/used-run":"{\"team\":\"t\",\"run_id\":\"%s\"}"}}}`, run, run))
		}
		items = append(items, `{"metadata":{"name":"node-free","annotations":{}}}`)
		return `{"items":[` + strings.Join(items, ",") + `]}`, nil
	case joined == "get nodeclaims -o json":
		var items []string
		for _, run := range f.triggered("deploy-smoke") {
			for i := 0; i < f.claimsPerRun; i++ {
				items = append(items, fmt.Sprintf(`{"metadata":{"name":"claim-%s-%d"},"status":{"nodeName":"node-%s"}}`, run, i, run))
			}
		}
		items = append(items, `{"metadata":{"name":"claim-free"},"status":{"nodeName":"node-free"}}`)
		return `{"items":[` + strings.Join(items, ",") + `]}`, nil
	case strings.Contains(joined, " logs "):
		if f.dirtyLogs {
			return "[pod/controller] level=WARN msg=slow\n", nil
		}
		return "[pod/controller] level=INFO msg=ready\n", nil
	}
	return "", fmt.Errorf("unexpected kubectl %s", joined)
}

func (f *fakeDeploy) triggered(pipeline string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var runs []string
	for _, call := range f.calls {
		fields := strings.Fields(call)
		if len(fields) > 3 && fields[1] == "pipeline" && fields[2] == "trigger" && fields[3] == pipeline {
			runs = append(runs, "run-"+pipeline+"-"+fields[slices.Index(fields, "--profile")+1])
		}
	}
	return runs
}

func TestDeployBuildPublishesThenReusesCandidate(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	images, err := d.buildCandidate(t.Context(), "v0.68.4-main.gtest")
	if err != nil {
		t.Fatal(err)
	}
	if images.Commit != f.head || images.SchemaDigest != f.old.SchemaDigest || images.Controller.Digest != testDigest("v0.68.4-main.gtestsparkwing-controller") {
		t.Fatalf("images = %+v", images)
	}
	if len(f.registry.published) != 1 || f.registry.published[0].Revision != f.head || f.registry.published[0].ControllerBase != d.cfg.ControllerBase {
		t.Fatalf("published = %+v", f.registry.published)
	}
	sums, err := os.ReadFile(filepath.Join(f.repo, "dist", "SHA256SUMS"))
	if err != nil || strings.Count(string(sums), "\n") != 4 {
		t.Fatalf("SHA256SUMS = %q, %v", sums, err)
	}

	again, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4-main.gtest")
	if err != nil || !again.sameDigests(images) || len(f.registry.published) != 1 {
		t.Fatalf("rebuild of a published commit = %+v, %v, published %d", again, err, len(f.registry.published))
	}

	for ref := range f.registry.revisions {
		if strings.Contains(ref, "sparkwing-runner@") {
			f.registry.revisions[ref] = "someone-else"
		}
	}
	if _, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4-main.gtest"); err == nil || !strings.Contains(err.Error(), "new --version") {
		t.Fatalf("a tag from another commit: %v", err)
	}
}

func TestDeployStagingGateThenProduction(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()

	staging, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, images)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pinsOnMain("sparkwing-staging"); got.Controller != images.Controller || got.Launcher != images.Runner || got.Job != images.Runner {
		t.Fatalf("staging pins = %+v", got)
	}
	if got := f.pinsOnMain("sparkwing"); got.Controller != f.old.Controller {
		t.Fatalf("staging deploy touched production: %+v", got)
	}
	if staging.Previous.Controller != f.old.Controller || len(staging.Changes) != 2 ||
		staging.Changes[0].Phase != deployPhaseController || staging.Changes[1].Phase != deployPhaseLauncher {
		t.Fatalf("staging record = %+v", staging)
	}
	subjects := f.subjects()
	if !strings.HasPrefix(subjects[1], "deploy(staging): controller") || !strings.HasPrefix(subjects[0], "deploy(staging): launcher") {
		t.Fatalf("GitOps history = %q", subjects)
	}
	if author := gitIn(t, f.bare, "log", "-1", "--format=%an <%ae>", "main"); author != "Sparkwing deploy <deploy@sparkwing.dev>" {
		t.Fatalf("deploy commit author = %q", author)
	}

	gate, err := f.deployer(false).stagingGate(t.Context(), images)
	if err != nil {
		t.Fatal(err)
	}
	if gate.ExampleRun != "run-example-staging" || gate.ReferenceRun != "run-hello-staging" || !gate.Images.sameDigests(images) {
		t.Fatalf("gate record = %+v", gate)
	}
	if !slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "pipeline trigger example -C "+f.repo) && strings.Contains(c, "--ref "+f.head)
	}) {
		t.Fatalf("example was not triggered at the candidate commit: %q", f.calls)
	}

	prod, err := f.deployer(false).promote(t.Context(), images)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pinsOnMain("sparkwing"); got.Controller != images.Controller || got.Launcher != images.Runner {
		t.Fatalf("production pins = %+v", got)
	}
	if prod.Environment != "production" || prod.Previous.Launcher != f.old.Runner || prod.WindowStart == "" {
		t.Fatalf("production record = %+v", prod)
	}
	if runs := f.triggered("deploy-smoke"); fmt.Sprint(runs) != "[run-deploy-smoke-qa-team run-deploy-smoke-qa-team-b]" {
		t.Fatalf("smoke runs = %v", runs)
	}

	again, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, images)
	if err != nil || len(again.Changes) != 0 {
		t.Fatalf("redeploying the same digests = %+v, %v", again, err)
	}
}

func TestDeployRollsBackFailedProductionSmoke(t *testing.T) {
	for name, breakIt := range map[string]func(*fakeDeploy){
		"failed run":   func(f *fakeDeploy) { f.failRuns["run-deploy-smoke-qa-team-b"] = true },
		"extra claim":  func(f *fakeDeploy) { f.claimsPerRun = 2 },
		"no claim":     func(f *fakeDeploy) { f.claimsPerRun = 0 },
		"warning logs": func(f *fakeDeploy) { f.dirtyLogs = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDeploy(t)
			breakIt(f)
			_, err := f.deployer(false).promote(t.Context(), f.candidate())
			if err == nil || !strings.Contains(err.Error(), "rolled back") {
				t.Fatalf("promote = %v, want a rollback", err)
			}
			if got := f.pinsOnMain("sparkwing"); got.Controller != f.old.Controller || got.Launcher != f.old.Runner || got.Job != f.old.Runner {
				t.Fatalf("production after rollback = %+v", got)
			}
			subjects := f.subjects()
			for i, want := range []string{`Revert "deploy(production): controller`, `Revert "deploy(production): launcher`, "deploy(production): launcher", "deploy(production): controller"} {
				if !strings.HasPrefix(subjects[i], want) {
					t.Fatalf("GitOps history = %q", subjects)
				}
			}
		})
	}
}

func TestDeployRefusesStoreBaselineChange(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	images.SchemaDigest = schemaBaselineDigestOf("CREATE TABLE other (id TEXT);\n")
	before := gitIn(t, f.bare, "rev-parse", "main")
	_, err := f.deployer(false).promote(t.Context(), images)
	if err == nil || !strings.Contains(err.Error(), "store change takes the full gate") {
		t.Fatalf("promote across a store change = %v", err)
	}
	if after := gitIn(t, f.bare, "rev-parse", "main"); after != before {
		t.Fatal("a refused deploy pushed to GitOps")
	}
}

func TestDeployDryRunPrintsEveryActionAndTakesNone(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(true)
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		t.Fatalf("dry run executed %s", c)
		return "", nil
	}
	d.tools.oidc = func(context.Context, string) (string, error) {
		t.Fatal("dry run minted an OIDC token")
		return "", nil
	}
	d.tools.registry = nil
	ctx := t.Context()
	images, err := d.buildCandidate(ctx, "v0.68.4-main.gdry")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.deployTo(ctx, d.cfg.Staging, images); err != nil {
		t.Fatal(err)
	}
	if _, err := d.stagingGate(ctx, images); err != nil {
		t.Fatal(err)
	}
	if _, err := d.promote(ctx, images); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"resolve the candidate commit", "assume arn:aws:iam::633280902600:role/sparkwing-deploy", "build linux/amd64", "build linux/arm64", "publish both images",
		"clone " + f.bare, "check that staging's store baseline", "pin the staging controller", "render sparkwing-staging/rendered.yaml",
		"commit \"deploy(staging): controller", "wait for Argo application sparkwing-staging", "wait for deployment sparkwing-staging/sparkwing-staging-sparkwing-full-controller",
		"pin the staging launcher", "wait for deployment sparkwing-staging/sparkwing-staging-sparkwing-runner-bundle-launcher",
		"build the candidate CLI", "add CLI profile staging", "trigger example", "trigger hello",
		"pin the production controller", "wait for Argo application sparkwing to be", "pin the production launcher",
		"add CLI profile qa-team ", "trigger deploy-smoke", "add CLI profile qa-team-b", "follow both smoke runs", "check each smoke run used one NodeClaim",
		"on a failed production deploy or smoke, would revert",
	}
	next := 0
	for _, line := range f.said {
		if !strings.HasPrefix(line, "dry-run: ") {
			t.Fatalf("dry run said %q without the dry-run prefix", line)
		}
		if next < len(want) && strings.Contains(line, want[next]) {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("dry run never printed %q in order; printed:\n%s", want[next], strings.Join(f.said, "\n"))
	}
	if _, err := os.Stat(filepath.Join(f.repo, "dist")); !os.IsNotExist(err) {
		t.Fatalf("dry run built into dist: %v", err)
	}
}
