package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func seedDeployConfigRepo(t *testing.T, root string, settings []byte) string {
	t.Helper()
	seed := filepath.Join(root, "config-seed")
	if err := os.MkdirAll(filepath.Join(seed, filepath.Dir(deploySettingsPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, deploySettingsPath), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	deployGitIn(t, seed, "init", "--quiet", "-b", "main")
	deployGitIn(t, seed, "add", "-A")
	deployGitIn(t, seed, "commit", "--quiet", "-m", "deploy settings")
	bare := filepath.Join(root, "config.git")
	deployGitIn(t, root, "clone", "--quiet", "--bare", seed, bare)
	local := filepath.Join(root, "deploy.yaml")
	if err := os.WriteFile(local, settings, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pipelinesEnvConfigRepo, "file://"+bare)
	t.Setenv(pipelinesEnvConfigParam, "/fixture/deploy/config-key")
	t.Setenv(pipelinesEnvDeployRole, testDeployRole)
	t.Setenv(pipelinesEnvDeployReg, "us-west-2")
	return bare
}

func isolatePipelinesSettings(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, key := range []string{pipelinesEnvConfigRepo, pipelinesEnvConfigParam, pipelinesEnvDeployRole, pipelinesEnvDeployReg} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	file, err := pipelinesEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDeploySettingsFillEveryAccountValue(t *testing.T) {
	settings, err := parseDeploySettings("fixture", fixtureDeploySettings(t, "git@github.com:example/gitops.git", "git@github.com:example/reference.git"))
	if err != nil {
		t.Fatal(err)
	}
	got := defaultDeployConfig()
	got.applySettings(settings)
	want := defaultDeployConfig()
	want.Registry = testRegistry
	want.Cluster = "fixture-cluster"
	want.ControllerBase = testRegistry + "/sparkwing-controller@sha256:" + strings.Repeat("1", 64)
	want.RunnerBase = testRegistry + "/sparkwing-runner@sha256:" + strings.Repeat("2", 64)
	want.GitopsURL = "git@github.com:example/gitops.git"
	want.ReferenceURL = "git@github.com:example/reference.git"
	want.Params = deployParams{
		GitopsKey:    "/fixture/deploy/gitops-deploy-key",
		ReferenceKey: "/fixture/deploy/reference-deploy-key",
		StagingToken: "/fixture/deploy/staging-gate-token",
		SmokeToken:   "/fixture/deploy/scan-submit-token",
	}
	want.Staging = deployEnv{
		Name: "staging", GitopsDir: "sparkwing-staging", HelmRelease: "sparkwing-staging", Namespace: "sparkwing-staging", ArgoApp: "sparkwing-staging",
		ControllerDeployment: "sparkwing-staging-sparkwing-full-controller", LauncherDeployment: "sparkwing-staging-sparkwing-runner-bundle-launcher",
		ControllerURL: "https://staging.sparkwing.dev", PrepullTag: "cloud-prepull-staging",
	}
	want.Production = deployEnv{
		Name: "production", GitopsDir: "sparkwing", HelmRelease: "sparkwing", Namespace: "sparkwing", ArgoApp: "sparkwing",
		ControllerDeployment: "sparkwing-sparkwing-full-controller", LauncherDeployment: "sparkwing-sparkwing-runner-bundle-launcher",
		ControllerURL: "https://api.sparkwing.dev", PrepullTag: "cloud-prepull",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied settings:\n got %+v\nwant %+v", got, want)
	}

	overridden := defaultDeployConfig()
	overridden.ControllerBase = "flag-controller-base"
	overridden.applySettings(settings)
	if overridden.ControllerBase != "flag-controller-base" || overridden.RunnerBase != want.RunnerBase {
		t.Fatalf("a --controller-base flag must win over the settings: %q, %q", overridden.ControllerBase, overridden.RunnerBase)
	}
}

func TestDeploySettingsRefuseIncompleteOrUnknownKeys(t *testing.T) {
	valid := string(fixtureDeploySettings(t, "git@github.com:example/gitops.git", "git@github.com:example/reference.git"))
	for name, tc := range map[string]struct {
		edit func(string) string
		want []string
	}{
		"missing keys": {
			func(s string) string {
				s = strings.Replace(s, "cluster: fixture-cluster\n", "", 1)
				return strings.Replace(s, "    prepull_tag: cloud-prepull\n", "", 1)
			},
			[]string{"missing cluster, environments.production.prepull_tag"},
		},
		"empty file":  {func(string) string { return "{}\n" }, []string{"missing registry, cluster, bases.controller"}},
		"unknown key": {func(s string) string { return s + "regoin: us-west-2\n" }, []string{"field regoin not found"}},
		"base by tag": {
			func(s string) string {
				return strings.Replace(s, "sparkwing-runner@sha256:"+strings.Repeat("2", 64), "sparkwing-runner:latest", 1)
			},
			[]string{"bases.runner", "by digest"},
		},
		"base outside the registry": {
			func(s string) string {
				return strings.Replace(s, "controller: 123456789012.dkr.ecr", "controller: 999999999999.dkr.ecr", 1)
			},
			[]string{"bases.controller", "must be an image in " + testRegistry},
		},
		"relative parameter": {
			func(s string) string { return strings.Replace(s, "gitops_key: /fixture", "gitops_key: fixture", 1) },
			[]string{"parameters.gitops_key", "not a parameter path"},
		},
		"shared gitops dir": {
			func(s string) string {
				return strings.Replace(s, "gitops_dir: sparkwing-staging", "gitops_dir: sparkwing", 1)
			},
			[]string{"share a gitops_dir"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseDeploySettings("fixture.yaml", []byte(tc.edit(valid)))
			if err == nil {
				t.Fatal("accepted")
			}
			for _, want := range append(tc.want, "deploy settings fixture.yaml") {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestDeployBootstrapComesFromMachineSettings(t *testing.T) {
	file := isolatePipelinesSettings(t)
	_, err := readDeployBootstrap()
	if err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("missing settings error = %v, want one naming %s", err, file)
	}
	for _, key := range []string{pipelinesEnvConfigRepo, pipelinesEnvConfigParam, pipelinesEnvDeployRole, pipelinesEnvDeployReg} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("missing settings error %q does not name %s", err, key)
		}
	}
	writeFile(t, file, strings.Join([]string{
		"# deploy runner",
		"export SPARKWING_PIPELINES_DEPLOY_CONFIG_REPO='git@github.com:example/config.git'",
		`SPARKWING_PIPELINES_DEPLOY_CONFIG_PARAMETER="/fixture/deploy/config-key"`,
		"SPARKWING_PIPELINES_DEPLOY_ROLE=" + testDeployRole,
		"SPARKWING_PIPELINES_DEPLOY_REGION=us-west-2",
		"",
	}, "\n"))
	got, err := readDeployBootstrap()
	want := deployBootstrap{ConfigRepo: "git@github.com:example/config.git", ConfigKey: "/fixture/deploy/config-key", RoleARN: testDeployRole, Region: "us-west-2"}
	if err != nil || got != want {
		t.Fatalf("file bootstrap = %+v, %v; want %+v", got, err, want)
	}
	t.Setenv(pipelinesEnvDeployReg, "eu-central-1")
	if got, err := readDeployBootstrap(); err != nil || got.Region != "eu-central-1" {
		t.Fatalf("the environment must win over the file: %+v, %v", got, err)
	}
	t.Setenv(pipelinesEnvDeployRole, "sparkwing-deploy")
	if _, err := readDeployBootstrap(); err == nil || !strings.Contains(err.Error(), "not an IAM role ARN") {
		t.Fatalf("a bare role name was accepted: %v", err)
	}
}

func TestDeploySignInReadsSettingsFromTheConfigurationRepository(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.cfg = defaultDeployConfig()
	d.cfg.ToolDir = ""
	if err := d.signIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.cfg, f.config()) {
		t.Fatalf("signed-in config:\n got %+v\nwant %+v", d.cfg, f.config())
	}
	calls := strings.Join(f.calls, "\n")
	for _, want := range []string{
		"aws sts assume-role-with-web-identity --role-arn " + testDeployRole,
		"aws ssm get-parameter --name /fixture/deploy/config-key",
		"git clone --quiet --depth 1 file://" + f.configRepo,
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("sign-in never ran %q; ran:\n%s", want, calls)
		}
	}
	if err := d.signIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(f.calls, "\n"), "git clone --quiet --depth 1 file://"+f.configRepo); n != 1 {
		t.Fatalf("a job cloned the configuration repository %d times", n)
	}
}

func TestDeploySignInRefusesInvalidRepositorySettings(t *testing.T) {
	f := newFakeDeploy(t)
	seedDeployConfigRepo(t, t.TempDir(), []byte("registry: "+testRegistry+"\n"))
	d := f.deployer(false)
	if err := d.signIn(t.Context()); err == nil || !strings.Contains(err.Error(), "missing cluster") || !strings.Contains(err.Error(), deploySettingsPath) {
		t.Fatalf("incomplete repository settings = %v", err)
	}
	if d.settled {
		t.Fatal("a refused load marked the settings loaded")
	}
}

func TestDeployDryRunReadsALocalSettingsFile(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(true)
	d.settingsFile = ""
	if err := d.signIn(t.Context()); err == nil || !strings.Contains(err.Error(), "--deploy-config") {
		t.Fatalf("a dry run without --deploy-config = %v", err)
	}
	d = f.deployer(true)
	if err := d.signIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "git clone") || strings.HasPrefix(c, "aws") }) {
		t.Fatalf("a dry run touched a credential or clone: %v", f.calls)
	}
	if d.cfg.Cluster != "fixture-cluster" {
		t.Fatalf("dry run settings were not read from %s: %+v", d.settingsFile, d.cfg)
	}
}

func TestDeployRefusesASettingsFileOutsideDryRun(t *testing.T) {
	host := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{}})
	if _, err := host.Plan(t.Context(), "Deploy", map[string]string{"deploy-config": "deploy.yaml"}); err == nil || !strings.Contains(err.Error(), "only with --dry-run") {
		t.Fatalf("a real deploy accepted --deploy-config: %v", err)
	}
}

func TestDeployFromRepositorySettingsReachesBothEnvironments(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	d := f.deployer(false)
	d.cfg = defaultDeployConfig()
	d.cfg.ToolDir = ""
	if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
		t.Fatal(err)
	}
	if got := f.pinsOnMain("sparkwing-staging"); got.Controller != images.Controller || got.Launcher != images.Runner {
		t.Fatalf("staging pins = %+v", got)
	}
	d.gate = stagingGateRecord{Images: images}
	if _, err := d.promote(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	if got := f.pinsOnMain("sparkwing"); got.Controller != images.Controller || got.Launcher != images.Runner {
		t.Fatalf("production pins = %+v", got)
	}
	calls := strings.Join(f.calls, "\n")
	for _, want := range []string{
		"eks update-kubeconfig --name fixture-cluster",
		"ssm get-parameter --name /fixture/deploy/gitops-deploy-key",
		"rollout status deployment/sparkwing-sparkwing-full-controller",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("the deploy never ran %q with the repository settings; ran:\n%s", want, calls)
		}
	}
	if tag := testRegistry + "/sparkwing-runner:cloud-prepull"; !slices.ContainsFunc(f.said, func(s string) bool { return strings.Contains(s, tag) }) {
		t.Fatalf("production never moved %s: %q", tag, f.said)
	}
}
