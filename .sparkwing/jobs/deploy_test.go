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

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
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

func TestDeployVersionOrder(t *testing.T) {
	versions := []string{
		"v0.68.2", "v0.68.3", "v0.68.3-9-gabababababab", "v0.68.3-10-g0123456789ab",
		"v0.68.4-0.20261106041046-718a7591f23a", "v0.68.4-0.20261106041047-0123456789ab", "v0.68.4",
	}
	for i := 1; i < len(versions); i++ {
		previous, err := deployVersionOrder(versions[i-1])
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := deployVersionOrder(versions[i])
		if err != nil || semver.Compare(candidate, previous) <= 0 {
			t.Fatalf("version order %s -> %s = %s -> %s, %v", versions[i-1], versions[i], previous, candidate, err)
		}
	}
	if zero, err := deployVersionOrder("v0.68.3-0-gabababababab"); err != nil || zero != "v0.68.3" {
		t.Fatalf("zero-distance stamp = %s, %v", zero, err)
	}
}

func TestDeployPseudoVersionUsesSemverOrder(t *testing.T) {
	version := "v0.69.3-0.20261106041046-718a7591f23a"
	ordered, err := deployVersionOrder(version)
	if err != nil || ordered != version || semver.Compare(version, "v0.69.2") <= 0 || semver.Compare(version, "v0.69.3") >= 0 {
		t.Fatalf("pseudo-version %q must sort between current and next release unchanged: %q, %v", version, ordered, err)
	}
}

func TestDeployRefValidation(t *testing.T) {
	for _, bad := range []string{"--upload-pack=touch /tmp/x", "-v", "abc", strings.Repeat("g", 40)} {
		if _, err := gitRevision(bad); err == nil {
			t.Errorf("gitRevision accepted %q", bad)
		}
	}
	if _, err := gitRevision(strings.Repeat("a", 40)); err != nil {
		t.Error(err)
	}
}

func TestSweepStaleDeployDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	dirs := map[string]time.Time{
		deployDirPrefix + "run-old-production-1": now.Add(-5 * time.Hour),
		deployDirPrefix + "run-new-production-2": now.Add(-time.Minute),
		"other-old":                              now.Add(-5 * time.Hour),
	}
	for name, mtime := range dirs {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(path, "inner"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "gitops-key"), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := sweepStaleDeployDirs(root, now, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	for name := range dirs {
		_, err := os.Stat(filepath.Join(root, name))
		if gone := os.IsNotExist(err); gone != (name == deployDirPrefix+"run-old-production-1") {
			t.Errorf("%s gone = %v", name, gone)
		}
	}
}

func TestSpliceControllerDeployment(t *testing.T) {
	old := testRendered("sparkwing", testPins("v1", testDigest("c1"), testDigest("r1")), "old-chart")
	next := testRendered("sparkwing", testPins("v2", testDigest("c2"), testDigest("r2")), "new-chart")
	spliced, err := spliceControllerDeployment(old, next, "sparkwing", "sparkwing-sparkwing-full-controller")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spliced, "@"+testDigest("c2")) || strings.Contains(spliced, "@"+testDigest("c1")) ||
		!strings.Contains(spliced, "@"+testDigest("r1")) || strings.Contains(spliced, "@"+testDigest("r2")) ||
		!strings.Contains(spliced, "chart: old-chart") || strings.Contains(spliced, "new-chart") {
		t.Fatalf("controller phase render:\n%s", spliced)
	}
	if _, err := spliceControllerDeployment(old, next, "sparkwing", "missing"); err == nil {
		t.Fatal("spliced a deployment neither render has")
	}
}

const testClusterScoped = `---
# Source: sparkwing-full/charts/sparkwing-runner-bundle/templates/launcher-rbac.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: sparkwing-cloud-jobs
---
# Source: sparkwing-full/charts/sparkwing-runner-bundle/templates/launcher-rbac.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sparkwing-runner-boundary
`

func TestDropClusterScoped(t *testing.T) {
	namespaced := testRendered("sparkwing", testPins("v1", testDigest("c1"), testDigest("r1")), "chart")
	kept, dropped, err := dropClusterScoped(namespaced + testClusterScoped)
	if err != nil {
		t.Fatal(err)
	}
	if kept != namespaced || fmt.Sprint(dropped) != "[Namespace/sparkwing-cloud-jobs ClusterRole/sparkwing-runner-boundary]" {
		t.Fatalf("kept:\n%s\ndropped: %q", kept, dropped)
	}
	unknown := namespaced + "---\napiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\n"
	if _, _, err := dropClusterScoped(unknown); err == nil || !strings.Contains(err.Error(), "Widget/w has no metadata.namespace") {
		t.Fatalf("an unknown kind without a namespace = %v", err)
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
	if _, ok := proven["staging-deploy"]; ok || proven["staging-gate"] != nil || fmt.Sprint(proven["production"]) != "[build-images staging-gate]" {
		t.Fatalf("promote-proven plan = %v", proven)
	}
	if _, err := host.Plan(t.Context(), "Deploy", map[string]string{"stop-after": "qa"}); err == nil {
		t.Fatal("an unknown --stop-after planned")
	}
}

func TestDeployPlanRequiresDeployRunner(t *testing.T) {
	for _, shape := range []struct {
		name string
		args map[string]string
		jobs int
	}{
		{"full", nil, 4},
		{"staging", map[string]string{"stop-after": "staging"}, 3},
		{"promote-proven", map[string]string{"promote-proven": "true"}, 3},
	} {
		for _, mode := range []string{"real", "dry-run", "sw-dry-run"} {
			t.Run(shape.name+"/"+mode, func(t *testing.T) {
				var want []string
				if mode == "real" {
					want = []string{"deploy-kikd-prod"}
				}
				args := map[string]string{}
				for key, value := range shape.args {
					args[key] = value
				}
				if mode == "dry-run" {
					args["dry-run"] = "true"
				}
				host := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{DryRun: mode == "sw-dry-run"}})
				plan, err := host.Plan(t.Context(), "Deploy", args)
				if err != nil {
					t.Fatal(err)
				}
				doc := decodePipeline(t, plan)
				if len(doc.Jobs) != shape.jobs {
					t.Fatalf("got %d jobs, want %d", len(doc.Jobs), shape.jobs)
				}
				for _, job := range doc.Jobs {
					if !slices.Equal(job.Requires, want) {
						t.Errorf("job %s requires %v, want %v", job.ID, job.Requires, want)
					}
				}
			})
		}
	}
}

func TestRequireStagingPass(t *testing.T) {
	images := deployImages{Controller: imagePin{Digest: testDigest("c")}, Runner: imagePin{Digest: testDigest("r")}}
	gate := sparkwing.Ref[stagingGateRecord]{JobID: "staging-gate"}
	ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"staging-gate": gateRecord(t, images, "")}}).Context(t.Context())
	if err := requireStagingPass(ctx, images, gate); err != nil {
		t.Fatalf("same digests: %v", err)
	}
	other := images
	other.Runner.Digest = testDigest("other")
	if err := requireStagingPass(ctx, other, gate); err == nil || !strings.Contains(err.Error(), "passed controller") {
		t.Fatalf("different digests: %v", err)
	}
	empty := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
	if err := requireStagingPass(empty, images, gate); err == nil || !strings.Contains(err.Error(), "no staging-gate record") {
		t.Fatalf("absent gate record: %v", err)
	}
}

func gateRecord(t *testing.T, images deployImages, gatedAt string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(stagingGateRecord{Images: images, RunID: "run-1", GatedAt: gatedAt})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProvenGateRecord(t *testing.T) {
	images := deployImages{Controller: imagePin{Digest: testDigest("c")}, Runner: imagePin{Digest: testDigest("r")}}
	proven := sparkwing.Ref[stagingGateRecord]{JobID: "staging-gate", Pipeline: "deploy"}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	latest := func(raw json.RawMessage) context.Context {
		return newTestHost(t, sparkwingtest.Options{Latest: func(_ context.Context, pipeline, node string) (json.RawMessage, error) {
			if pipeline != "deploy" || node != "staging-gate" {
				return nil, fmt.Errorf("unexpected reference %s/%s", pipeline, node)
			}
			return raw, nil
		}}).Context(t.Context())
	}
	rec, err := provenGateRecord(latest(gateRecord(t, images, "2026-11-01T12:00:00Z")), proven, now, 48*time.Hour)
	if err != nil || !rec.Images.sameDigests(images) {
		t.Fatalf("recent record = %+v, %v", rec, err)
	}
	if _, err := provenGateRecord(latest(gateRecord(t, images, "2026-10-01T12:00:00Z")), proven, now, 48*time.Hour); err == nil || !strings.Contains(err.Error(), "longer ago") {
		t.Fatalf("old record: %v", err)
	}
	if _, err := provenGateRecord(latest(gateRecord(t, images, "")), proven, now, 48*time.Hour); err == nil {
		t.Fatal("a record without a gate time promoted")
	}
	if _, err := provenGateRecord(newTestHost(t, sparkwingtest.Options{}).Context(t.Context()), proven, now, 48*time.Hour); err == nil {
		t.Fatal("no recorded run promoted")
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
	sessions     int
	expired      int
	oidcDown     bool
	onSmoke      func(run string) error
}

func testPins(version, ctl, run string) valuesPins {
	return valuesPins{
		Controller: imagePin{testRegistry + "/sparkwing-controller", version, ctl},
		Launcher:   imagePin{testRegistry + "/sparkwing-runner", version, run},
		Job:        imagePin{testRegistry + "/sparkwing-runner", version, run},
	}
}

func testRendered(release string, pins valuesPins, chart string) string {
	return `---
# Source: sparkwing-full/templates/controller.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + release + `-sparkwing-full-controller
  namespace: ` + release + `
spec:
  template:
    spec:
      containers:
        - name: controller
          image: "` + pins.Controller.ref() + `"
---
# Source: sparkwing-full/charts/sparkwing-runner-bundle/templates/launcher.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + release + `-sparkwing-runner-bundle-launcher
  namespace: ` + release + `
spec:
  template:
    spec:
      containers:
        - name: launcher
          image: "` + pins.Launcher.ref() + `"
          args:
            - "--image=` + pins.Job.Repository + ":" + pins.Job.Tag + "@" + pins.Job.Digest + `"
---
# Source: sparkwing-full/templates/chart.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sparkwing-chart
  namespace: ` + release + `
data:
  chart: ` + chart + `
`
}

func (f *fakeDeploy) session(env map[string]string) error {
	var n int
	if _, err := fmt.Sscanf(env["AWS_SESSION_TOKEN"], "session-%d", &n); err != nil || n == 0 {
		return fmt.Errorf("no role session")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n <= f.expired {
		return fmt.Errorf("ExpiredToken: session-%d has expired", n)
	}
	return nil
}

func (f *fakeDeploy) commitValues(dir, values string) {
	f.t.Helper()
	work := filepath.Join(f.t.TempDir(), "edit")
	deployGitIn(f.t, filepath.Dir(work), "clone", "--quiet", f.bare, work)
	if err := os.WriteFile(filepath.Join(work, dir, "helm-values.yaml"), []byte(values), 0o644); err != nil {
		f.t.Fatal(err)
	}
	deployGitIn(f.t, work, "commit", "--quiet", "-am", "hand edit")
	deployGitIn(f.t, work, "push", "--quiet", "origin", "HEAD:main")
}

func deployGitIn(t *testing.T, dir string, args ...string) string {
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
	deployGitIn(t, f.repo, "init", "--quiet", "-b", "main")
	deployGitIn(t, f.repo, "add", "-A")
	deployGitIn(t, f.repo, "commit", "--quiet", "-m", "source")
	f.head = deployGitIn(t, f.repo, "rev-parse", "HEAD")
	deployGitIn(t, root, "clone", "--quiet", "--bare", f.repo, filepath.Join(root, "source.git"))
	deployGitIn(t, f.repo, "remote", "add", "origin", filepath.Join(root, "source.git"))

	f.old = deployImages{
		Commit: f.head, Version: "v0.68.3", SchemaDigest: schemaBaselineDigestOf(schema),
		Controller: imagePin{testRegistry + "/sparkwing-controller", "v0.68.3", testDigest("old-ctl")},
		Runner:     imagePin{testRegistry + "/sparkwing-runner", "v0.68.3", testDigest("old-run")},
	}
	f.registry = &fakeDeployRegistry{digests: map[string]string{}, revisions: map[string]string{
		f.old.Controller.Repository + "@" + f.old.Controller.Digest: f.head,
	}}
	for _, ref := range []string{f.config().ControllerBase, f.config().RunnerBase} {
		f.registry.digests[ref] = testDigest(ref)
	}

	work := filepath.Join(root, "gitops-seed")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	deployGitIn(t, work, "init", "--quiet", "-b", "main")
	for _, dir := range []string{"sparkwing", "sparkwing-staging"} {
		values := testValues("v0.68.3", f.old.Controller.Digest, f.old.Runner.Digest)
		rendered := testRendered(dir, testPins("v0.68.3", f.old.Controller.Digest, f.old.Runner.Digest), "old-chart")
		if err := os.MkdirAll(filepath.Join(work, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"helm-values.yaml": values, "rendered.yaml": rendered} {
			if err := os.WriteFile(filepath.Join(work, dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	deployGitIn(t, work, "add", "-A")
	deployGitIn(t, work, "commit", "--quiet", "-m", "seed")
	deployGitIn(t, root, "clone", "--quiet", "--bare", work, f.bare)
	return f
}

func (f *fakeDeploy) candidate() deployImages {
	c := f.old
	c.Version = "v0.68.3-1-g" + f.head[:12]
	c.Controller = imagePin{c.Controller.Repository, c.Version, testDigest("new-ctl")}
	c.Runner = imagePin{c.Runner.Repository, c.Version, testDigest("new-run")}
	return c
}

func (f *fakeDeploy) config() deployConfig {
	cfg := defaultDeployConfig()
	cfg.Registry = testRegistry
	cfg.GitopsURL = f.bare
	cfg.ReferenceURL = f.repo
	cfg.ToolDir = ""
	return cfg
}

func (f *fakeDeploy) deployer(dry bool) *deployer {
	tools := deployTools{
		run:      f.run,
		registry: f.registry,
		oidc: func(context.Context, string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.oidcDown {
				return "", errors.New("the run's claim has ended")
			}
			return "oidc-token", nil
		},
		wait:     func(context.Context, time.Duration) error { return nil },
		lookPath: func(name string) (string, error) { return name, nil },
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
	pins, err := readValuesPins(deployGitIn(f.t, f.bare, "show", "main:"+dir+"/helm-values.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	return pins
}

func (f *fakeDeploy) subjects() []string {
	return strings.Split(deployGitIn(f.t, f.bare, "log", "--format=%s", "main"), "\n")
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
			if _, set := c.Env["AWS_PROFILE"]; set || c.Env["AWS_CONFIG_FILE"] == "" {
				return "", fmt.Errorf("sts call can read the machine's AWS config: %v", c.Env)
			}
			f.mu.Lock()
			f.sessions++
			n, expires := f.sessions, f.clock.Add(time.Hour).Format(time.RFC3339)
			f.mu.Unlock()
			return fmt.Sprintf(`{"Credentials":{"AccessKeyId":"AKIA","SecretAccessKey":"secret","SessionToken":"session-%d","Expiration":%q}}`, n, expires), nil
		case strings.HasPrefix(joined, "ssm get-parameter"):
			if err := f.session(c.Env); err != nil {
				return "", err
			}
			return "value-of-" + arg("--name") + "\n", nil
		case strings.HasPrefix(joined, "ecr get-login-password"):
			return "ecr-password\n", nil
		case strings.HasPrefix(joined, "eks update-kubeconfig"):
			return "", nil
		}
	case name == "helm":
		body, err := os.ReadFile(arg("-f"))
		if err != nil {
			return "", err
		}
		pins, err := readValuesPins(string(body))
		if err != nil {
			return "", err
		}
		return testRendered(c.Args[1], pins, "new-chart") + testClusterScoped, nil
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
			if f.onSmoke != nil && strings.Contains(arg("--run"), "deploy-smoke") {
				if err := f.onSmoke(arg("--run")); err != nil {
					return "", err
				}
			}
			if f.failRuns[arg("--run")] {
				return "", fmt.Errorf("exit 1")
			}
			return `{"status":"success"}`, nil
		}
	}
	return "", fmt.Errorf("unexpected command %s", c)
}

func (f *fakeDeploy) kubectl(c deployCommand) (string, error) {
	if err := f.session(c.Env); err != nil {
		return "", err
	}
	joined := strings.Join(c.Args, " ")
	envDir := map[string]string{"sparkwing-staging": "sparkwing-staging", "sparkwing": "sparkwing"}
	switch {
	case strings.Contains(joined, "rollout status"):
		return "", nil
	case strings.Contains(joined, "get application"):
		app := c.Args[len(c.Args)-3]
		if _, ok := envDir[app]; !ok {
			return "", fmt.Errorf("unknown app %s", app)
		}
		head := deployGitIn(f.t, f.bare, "rev-parse", "main")
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
	images, err := d.buildCandidate(t.Context(), "v0.68.3-1-g0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	if images.Commit != f.head || images.SchemaDigest != f.old.SchemaDigest || images.Controller.Digest != testDigest("v0.68.3-1-g0123456789absparkwing-controller") {
		t.Fatalf("images = %+v", images)
	}
	if len(f.registry.published) != 1 || f.registry.published[0].Revision != f.head || f.registry.published[0].ControllerBase != d.cfg.ControllerBase {
		t.Fatalf("published = %+v", f.registry.published)
	}
	sums, err := os.ReadFile(filepath.Join(f.repo, "dist", "SHA256SUMS"))
	if err != nil || strings.Count(string(sums), "\n") != 4 {
		t.Fatalf("SHA256SUMS = %q, %v", sums, err)
	}

	delete(f.registry.digests, f.config().ControllerBase)
	delete(f.registry.digests, f.config().RunnerBase)
	again, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.3-1-g0123456789ab")
	if err != nil || !again.sameDigests(images) || len(f.registry.published) != 1 {
		t.Fatalf("rebuild of a published commit = %+v, %v, published %d", again, err, len(f.registry.published))
	}

	for ref := range f.registry.revisions {
		if strings.Contains(ref, "sparkwing-runner@") {
			f.registry.revisions[ref] = "someone-else"
		}
	}
	if _, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.3-1-g0123456789ab"); err == nil || !strings.Contains(err.Error(), "new --version") {
		t.Fatalf("a tag from another commit: %v", err)
	}
}

func TestDeployMissingRuntimeBaseStopsBeforeBuild(t *testing.T) {
	for _, base := range []string{"controller", "runner"} {
		t.Run(base, func(t *testing.T) {
			f := newFakeDeploy(t)
			ref := f.config().ControllerBase
			if base == "runner" {
				ref = f.config().RunnerBase
			}
			delete(f.registry.digests, ref)
			_, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4")
			if err == nil || !strings.Contains(err.Error(), "runtime base "+ref+" is missing") {
				t.Fatalf("missing runtime base: %v", err)
			}
			if len(f.registry.published) != 0 || slices.ContainsFunc(f.calls, func(call string) bool {
				return strings.Contains(call, "bin/build-")
			}) {
				t.Fatalf("missing runtime base reached a build: %v", f.calls)
			}
		})
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
	if author := deployGitIn(t, f.bare, "log", "-1", "--format=%an <%ae>", "main"); author != "Sparkwing deploy <deploy@sparkwing.dev>" {
		t.Fatalf("deploy commit author = %q", author)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "annotate") || strings.Contains(c, "patch") }) {
		t.Fatalf("the deploy wrote to an Argo application: %q", f.calls)
	}
	controllerRender := deployGitIn(t, f.bare, "show", "main~1:sparkwing-staging/rendered.yaml")
	if !strings.Contains(controllerRender, "@"+images.Controller.Digest) || !strings.Contains(controllerRender, "@"+f.old.Runner.Digest) ||
		!strings.Contains(controllerRender, "chart: old-chart") {
		t.Fatalf("the controller commit changed more than the controller Deployment:\n%s", controllerRender)
	}
	if final := deployGitIn(t, f.bare, "show", "main:sparkwing-staging/rendered.yaml"); !strings.Contains(final, "chart: new-chart") || strings.Contains(final, "@"+f.old.Runner.Digest) {
		t.Fatalf("the launcher commit did not carry the rest of the chart:\n%s", final)
	} else if strings.Contains(final, "kind: ClusterRole") || strings.Contains(final, "kind: Namespace") {
		t.Fatalf("the render kept cluster-scoped objects:\n%s", final)
	}
	if !slices.Contains(f.said, "left out of sparkwing-staging/rendered.yaml, applied through kikd-infra k8s/sparkwing-cluster/: Namespace/sparkwing-cloud-jobs, ClusterRole/sparkwing-runner-boundary") {
		t.Fatalf("the deploy did not name the cluster-scoped objects it left out: %q", f.said)
	}

	gate, err := f.deployer(false).stagingGate(t.Context(), images)
	if err != nil {
		t.Fatal(err)
	}
	if gate.ExampleRun != "run-example-staging" || gate.ReferenceRun != "run-hello-staging" || !gate.Images.sameDigests(images) {
		t.Fatalf("gate record = %+v", gate)
	}
	if !slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "pipeline trigger example -C "+f.repo) && strings.Contains(c, "--ref "+f.head) && strings.HasSuffix(c, "-- --api-controller "+defaultDeployConfig().Staging.ControllerURL)
	}) {
		t.Fatalf("example was not triggered at the candidate commit with the staging controller for its probes: %q", f.calls)
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
	if err != nil || len(again.Changes) != 0 || again.WindowStart == "" {
		t.Fatalf("redeploying the same digests = %+v, %v", again, err)
	}
	if _, err := f.deployer(false).promote(t.Context(), images); err != nil {
		t.Fatalf("promoting digests production already runs: %v", err)
	}
	if !slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, " logs ") && !strings.Contains(c, "--since-time= ") }) {
		t.Fatalf("the log check ran without a window: %q", f.calls)
	}
}

func TestDeployToolchain(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.cfg.ToolDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(d.cfg.ToolDir, "aws"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var seen deployCommand
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		seen = c
		return "", nil
	}
	if _, err := d.cmd(t.Context(), deployCommand{Name: "aws", Env: map[string]string{"A": "b"}}); err != nil {
		t.Fatal(err)
	}
	if seen.Name != filepath.Join(d.cfg.ToolDir, "aws") || seen.Env["PATH"] != d.cfg.ToolDir+":/usr/bin:/bin" || seen.Env["A"] != "b" {
		t.Fatalf("command = %+v", seen)
	}
	if _, err := d.cmd(t.Context(), deployCommand{Name: "kubectl"}); err == nil || !strings.Contains(err.Error(), "deploy toolchain") {
		t.Fatalf("a tool missing from the toolchain: %v", err)
	}
}

func TestDeployRefusesMissingToolchain(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "bin")
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.cfg.ToolDir = missing
	if _, err := d.cmd(t.Context(), deployCommand{Name: "aws"}); err == nil || !strings.Contains(err.Error(), missing+" is missing") || !strings.Contains(err.Error(), "Deploy toolchain") {
		t.Fatalf("a real deploy without its toolchain: %v", err)
	}
	job := deployJob{Cfg: f.config(), Name: "production"}
	job.Cfg.ToolDir = missing
	if _, _, err := job.deployer(); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a real deploy job without its toolchain: %v", err)
	}
	dry := f.deployer(true)
	dry.cfg.ToolDir = missing
	if path, err := dry.tool("git"); err != nil || path != "git" {
		t.Fatalf("a dry run previews from PATH: %q %v", path, err)
	}
}

func TestDeployRefusesSupersededCommit(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	if err := os.WriteFile(filepath.Join(f.repo, "NEXT"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deployGitIn(t, f.repo, "add", "NEXT")
	deployGitIn(t, f.repo, "commit", "--quiet", "-m", "next")
	deployGitIn(t, f.repo, "push", "--quiet", "origin", "HEAD:main")
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	for _, env := range []deployEnv{f.config().Staging, f.config().Production} {
		if _, _, err := f.deployer(false).deployTo(t.Context(), env, images); err == nil || !strings.Contains(err.Error(), "only main's tip deploys") {
			t.Fatalf("%s deploy of a superseded commit: %v", env.Name, err)
		}
	}
	if deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("a superseded commit reached GitOps")
	}
}

func TestDeployRefusesProductionDowngrade(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	images.Version = "v0.68.2"
	images.Controller.Tag, images.Runner.Tag = images.Version, images.Version
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	if _, err := f.deployer(false).promote(t.Context(), images); err == nil || !strings.Contains(err.Error(), "never moves an environment back") {
		t.Fatalf("production downgrade: %v", err)
	}
	if deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("a downgrade reached GitOps")
	}
}

func TestDeployFirstDeployOverUnversionedImageNeedsAPerson(t *testing.T) {
	f := newFakeDeploy(t)
	f.commitValues("sparkwing", testValues("commit-9fe8aca9f", f.old.Controller.Digest, f.old.Runner.Digest))
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	_, err := f.deployer(false).promote(t.Context(), f.candidate())
	if err == nil || !strings.Contains(err.Error(), "Deploy once by hand") || !strings.Contains(err.Error(), "helm template sparkwing charts/sparkwing-full --namespace sparkwing -f sparkwing/helm-values.yaml | yq 'select(.metadata.namespace != null)'") {
		t.Fatalf("first deploy over an unversioned image: %v", err)
	}
	if deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("a refused first deploy reached GitOps")
	}
}

type expiringContext struct {
	context.Context
	done chan struct{}
}

func (c expiringContext) Done() <-chan struct{} { return c.done }

func (c expiringContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestDeployRollbackOutlivesCancelTimeoutAndSession(t *testing.T) {
	for name, setup := range map[string]func(*fakeDeploy) context.Context{
		"cancelled job": func(f *fakeDeploy) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			f.onSmoke = func(string) error {
				cancel()
				f.mu.Lock()
				f.oidcDown = true
				f.mu.Unlock()
				return context.Canceled
			}
			return ctx
		},
		"job timeout": func(f *fakeDeploy) context.Context {
			ctx := expiringContext{Context: t.Context(), done: make(chan struct{})}
			var once sync.Once
			f.onSmoke = func(string) error {
				once.Do(func() { close(ctx.done) })
				return context.DeadlineExceeded
			}
			return ctx
		},
		"expired session": func(f *fakeDeploy) context.Context {
			f.onSmoke = func(string) error {
				f.mu.Lock()
				f.expired = f.sessions
				f.mu.Unlock()
				return errors.New("exit 1")
			}
			return t.Context()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDeploy(t)
			ctx := setup(f)
			_, err := f.deployer(false).promote(ctx, f.candidate())
			if err == nil || !strings.Contains(err.Error(), "rolled back") {
				t.Fatalf("promote = %v, want a rollback", err)
			}
			if got := f.pinsOnMain("sparkwing"); got.Controller != f.old.Controller || got.Launcher != f.old.Runner {
				t.Fatalf("production after rollback = %+v", got)
			}
		})
	}
}

func TestDeploySmokeLogCheckRenewsAnExpiredSession(t *testing.T) {
	f := newFakeDeploy(t)
	// safety: both smoke runs are followed concurrently, so expiring on each status read
	// could expire the renewed session too, depending on which run reports first.
	var once sync.Once
	f.onSmoke = func(string) error {
		once.Do(func() {
			f.mu.Lock()
			f.expired = f.sessions
			f.mu.Unlock()
		})
		return nil
	}
	if _, err := f.deployer(false).promote(t.Context(), f.candidate()); err != nil {
		t.Fatalf("promote across a session expiry: %v", err)
	}
	if got := f.pinsOnMain("sparkwing"); got.Controller != f.candidate().Controller {
		t.Fatalf("production = %+v", got)
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

func TestDeployRollsOutChangedStoreBaselineWithoutRDS(t *testing.T) {
	for _, envName := range []string{"staging", "production"} {
		t.Run(envName, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			env := d.cfg.Staging
			if envName == "production" {
				env = d.cfg.Production
			}
			images := f.candidate()
			images.SchemaDigest = schemaBaselineDigestOf("CREATE TABLE other (id TEXT);\n")
			rec, _, err := d.deployTo(t.Context(), env, images)
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.Changes) != 2 || f.pinsOnMain(env.GitopsDir).Controller != images.Controller {
				t.Fatalf("deploy with a changed baseline = %+v", rec)
			}
		})
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
	images, err := d.buildCandidate(ctx, "v0.68.3-1-g0123456789ab")
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
		"clone " + f.bare, "pin the staging controller", "render sparkwing-staging/rendered.yaml",
		"commit \"deploy(staging): controller", "wait for Argo application sparkwing-staging", "wait for deployment sparkwing-staging/sparkwing-staging-sparkwing-full-controller",
		"pin the staging launcher", "wait for deployment sparkwing-staging/sparkwing-staging-sparkwing-runner-bundle-launcher",
		"build the candidate CLI", "add CLI profile staging", "trigger example", "trigger hello",
		"is still the tip of origin/main", "is not older than the production version",
		"pin the production controller", "wait for Argo application sparkwing to be", "pin the production launcher",
		"add CLI profile qa-team ", "trigger deploy-smoke", "add CLI profile qa-team-b", "follow both smoke runs", "check each smoke run used one NodeClaim",
		"on a failed production deploy or smoke, would renew the role session and revert",
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

// Decision 87: the deploy credential reaches only the Sparkwing GitOps repository.
func TestDeployWritesOnlySparkwingGitops(t *testing.T) {
	cfg := defaultDeployConfig()
	if cfg.GitopsURL != "git@github.com:sparkwing-dev/sparkwing-gitops.git" || cfg.Params.GitopsKey != "/kikd-prod/sparkwing/deploy/sparkwing-gitops-deploy-key" {
		t.Fatalf("GitOps target = %q with key %q", cfg.GitopsURL, cfg.Params.GitopsKey)
	}
}

func TestDeployBuildsUntaggedMainWithoutARelease(t *testing.T) {
	f := newFakeDeploy(t)
	deployGitIn(t, f.repo, "tag", "v0.68.3")
	deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "main change")
	f.head = deployGitIn(t, f.repo, "rev-parse", "HEAD")
	d := f.deployer(false)
	images, err := d.buildCandidate(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := time.Parse(time.RFC3339, deployGitIn(t, f.repo, "show", "-s", "--format=%cI", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	want := "v0.68.4-0." + committed.UTC().Format("20060102150405") + "-" + f.head[:12]
	if images.Version != want || len(f.registry.published) != 1 {
		t.Fatalf("untagged main build = %+v, publications=%v", images, f.registry.published)
	}
	if images.Controller.Tag != want || images.Runner.Tag != want || f.registry.published[0].Version != want {
		t.Fatalf("candidate version differs across images: %+v, publication=%+v", images, f.registry.published[0])
	}
	if tags := deployGitIn(t, f.repo, "tag", "--points-at", "HEAD"); tags != "" {
		t.Fatalf("deploy requires a release tag at HEAD: %s", tags)
	}
	if !slices.ContainsFunc(f.calls, func(call string) bool { return strings.Contains(call, "bin/build-web.sh --reuse") }) {
		t.Fatal("deploy did not request validated dashboard reuse")
	}
	_, err = d.buildCandidate(t.Context(), "")
	if err != nil || len(f.registry.published) != 1 {
		t.Fatalf("same main commit should reuse published images: %v, %v", f.registry.published, err)
	}
}

func TestDeployRepositoryPushStopsAfterStaging(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cfg, err := pipelines.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Find("deploy")
	if entry == nil || entry.Args["stop-after"] != "staging" || entry.On.Push == nil || !slices.Equal(entry.On.Push.Branches, []string{"main"}) {
		t.Fatalf("push deploy configuration = %+v", entry)
	}
}

func TestDeployStagingGateStartsBothRunsBeforeWaiting(t *testing.T) {
	f := newFakeDeploy(t)
	if _, err := f.deployer(false).stagingGate(t.Context(), f.candidate()); err != nil {
		t.Fatal(err)
	}
	firstWait := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "runs status") })
	reference := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "pipeline trigger hello") })
	if reference < 0 || firstWait <= reference {
		t.Fatalf("staging probes must overlap: %v", f.calls)
	}
}

func TestDeployBuildsReleaseTagUnchanged(t *testing.T) {
	f := newFakeDeploy(t)
	deployGitIn(t, f.repo, "tag", "-a", "v0.69.2", "-m", "release")
	images, err := f.deployer(false).buildCandidate(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if images.Version != "v0.69.2" || images.Controller.Tag != "v0.69.2" || images.Runner.Tag != "v0.69.2" || f.registry.published[0].Version != "v0.69.2" {
		t.Fatalf("release version changed: %+v, publications=%+v", images, f.registry.published)
	}
}
