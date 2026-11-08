package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	sparkwing "github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestTemplateVerifyCLIPathUsesPlatformExecutableName(t *testing.T) {
	dir := filepath.Join("scratch with spaces", "cli")
	for _, tc := range []struct {
		goos string
		name string
	}{
		{goos: "windows", name: "sparkwing.exe"},
		{goos: "linux", name: "sparkwing"},
		{goos: "darwin", name: "sparkwing"},
	} {
		if got, want := templateVerifyCLIPath(dir, tc.goos), filepath.Join(dir, tc.name); got != want {
			t.Errorf("%s executable = %q, want %q", tc.goos, got, want)
		}
	}
}

func TestTemplateVerifierCompilesAndPlansFreshTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI and a fresh template")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := templateVerifyCLIPath(t.TempDir(), runtime.GOOS)
	build := exec.CommandContext(t.Context(), "go", "build", "-p", "1", "-o", bin, "./cmd/sparkwing")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build verifier CLI: %v\n%s", err, output)
	}
	ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
	for key, value := range readGoEnv(ctx) {
		t.Setenv(key, value)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	var manifest templates.Manifest
	for _, candidate := range verifyTemplates {
		if candidate.Manifest.Name == "lint-test-go" {
			manifest = candidate.Manifest
			break
		}
	}
	if manifest.Name == "" {
		t.Fatal("lint-test-go verification template is missing")
	}
	manifest.Verify = templates.VerifyCompileOnly
	outcome, err := verifyTemplate(ctx, manifest, verifyEnv{CLI: bin, Root: root})
	if err != nil || outcome.Tier != templates.VerifyCompileOnly || outcome.Skipped {
		t.Fatalf("fresh-template verification: %+v: %v", outcome, err)
	}
}

func TestSeedFixture_WritesExpectedFiles(t *testing.T) {
	cases := map[string][]string{
		templates.FixtureNone:         nil,
		templates.FixtureGoModule:     {"go.mod", "main.go", "main_test.go"},
		templates.FixtureDocker:       {"go.mod", "Dockerfile", ".dockerignore"},
		templates.FixtureNodeModule:   {"package.json", "package-lock.json", filepath.Join("test", "smoke.test.js")},
		templates.FixturePythonModule: {"pyproject.toml", filepath.Join("verify_fixture", "__init__.py"), "test_smoke.py"},
	}
	for fixture, want := range cases {
		dir := t.TempDir()
		if err := seedFixture(dir, fixture); err != nil {
			t.Fatalf("seedFixture(%q): %v", fixture, err)
		}
		for _, rel := range want {
			if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
				t.Errorf("fixture %q: missing %s: %v", fixture, rel, err)
			}
		}
	}
}

func TestTemplateRunsShareOneDaemonOutsideTemplateScratch(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "shared-state")
	first := templateRunAdmissionEnv(stateHome)["SPARKWING_HOME"]
	second := templateRunAdmissionEnv(stateHome)["SPARKWING_HOME"]
	if first != stateHome || second != stateHome {
		t.Fatalf("template run homes = %q, %q; want shared %q", first, second, stateHome)
	}
}

func TestNormalizeVerifyModulePath_IsStableAcrossScratchDirectories(t *testing.T) {
	var got []string
	for _, initial := range []string{"sparkwing-tv-example-123-pipelines", "sparkwing-tv-example-987-pipelines"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(path, []byte("module "+initial+"\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mainPath := filepath.Join(dir, "main.go")
		if err := os.WriteFile(mainPath, []byte("package main\nimport _ \""+initial+"/jobs\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := normalizeVerifyModulePath(dir, "example"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(data))
		mainData, err := os.ReadFile(mainPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mainData), initial) || !strings.Contains(string(mainData), "example.com/sparkwing/verify/example/pipelines/jobs") {
			t.Fatalf("generated import was not normalized: %q", mainData)
		}
	}
	if got[0] != got[1] || !strings.Contains(got[0], "module example.com/sparkwing/verify/example/pipelines") {
		t.Fatalf("normalized modules differ or are not stable: %q, %q", got[0], got[1])
	}
}

func TestCleanupTemplateScratch_RemovesOnlyVerifierDirectories(t *testing.T) {
	root := t.TempDir()
	staleTemplate := filepath.Join(root, "sparkwing-tv-example-123")
	staleCLI := filepath.Join(root, "sparkwing-template-verify-cli-123")
	retained := []string{
		filepath.Join(root, "other-tool-123"),
		filepath.Join(root, "sparkwing-other-123"),
		filepath.Join(root, "sparkwing-tv"),
		filepath.Join(root, "sparkwing-template-verify-cli"),
		filepath.Join(root, "sparkwing-template-verify-cliother-123"),
	}
	for _, dir := range append([]string{staleTemplate, staleCLI}, retained...) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupTemplateScratch(root); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{staleTemplate, staleCLI} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("verifier scratch %s remains: %v", filepath.Base(stale), err)
		}
	}
	for _, dir := range retained {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("unrelated temp data %s was removed: %v", filepath.Base(dir), err)
		}
	}
}

func TestSeedFixture_RejectsUnknown(t *testing.T) {
	if err := seedFixture(t.TempDir(), "rust-crate"); err == nil {
		t.Fatal("expected error for unknown fixture")
	}
}

func TestGoModuleFixture_HasCoverableStatements(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	if err := seedGoModule(dir); err != nil {
		t.Fatal(err)
	}
	prof := filepath.Join(dir, "cover.out")
	cmd := exec.Command("go", "test", "-coverprofile="+prof, "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	data, err := os.ReadFile(prof)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("coverprofile has no statement lines (zero total statements):\n%s", data)
	}
}

func TestSeedMigrations_WritesReversiblePair(t *testing.T) {
	dir := t.TempDir()
	if err := seedMigrations(dir, "db/migrations"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"db/migrations/0001_init.up.sql", "db/migrations/0001_init.down.sql"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
}

func TestTemplateSecretsEnv_KeepsTheKeyInTheSharedStateHome(t *testing.T) {
	env := templateSecretsEnv("/state", "/scratch/home")
	if env["SPARKWING_HOME"] != "/state" || env["HOME"] != "/scratch/home" {
		t.Fatalf("env = %v, want the state home and the scratch home", env)
	}
	if got := env["SPARKWING_SECRETS_KEY_FILE"]; got != filepath.Join("/state", "secrets.key") {
		t.Fatalf("key file = %q, want it inside the shared state home", got)
	}
}

func TestFixtureToolchainReady_GoAndNoneAlwaysReady(t *testing.T) {
	for _, fixture := range []string{templates.FixtureNone, templates.FixtureGoModule} {
		if ok, missing := fixtureToolchainReady(context.Background(), fixture); !ok {
			t.Errorf("fixture %q should be ready, missing=%q", fixture, missing)
		}
	}
}

func TestNodeFixture_PassesNpm(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	dir := t.TempDir()
	if err := seedNodeModule(dir); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ci"}, {"test"}, {"run", "lint"}} {
		result, err := sparkwing.Bash(newTestContext(t), "npm "+strings.Join(args, " ")).Dir(dir).Capture()
		if err != nil {
			t.Fatalf("npm %v: %v\n%s\n%s", args, err, result.Stdout, result.Stderr)
		}
	}
}

func TestPythonFixture_PassesUnittest(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	if err := seedPythonModule(dir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-m", "unittest", "discover")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python3 -m unittest discover: %v\n%s", err, out)
	}
}

func TestTemplateDryRunEvidenceControlsProofRecording(t *testing.T) {
	for _, tc := range []struct {
		name, jobs, steps string
		partial           bool
	}{
		{
			name:    "job without preview",
			jobs:    `[{"id":"deploy","status":"done","outcome":"skipped","status_detail":"no_dry_run_defined"}]`,
			steps:   `[]`,
			partial: true,
		},
		{
			name:    "successful job with skipped body",
			jobs:    `[{"id":"deploy","status":"done","outcome":"success"}]`,
			steps:   `[{"job_id":"deploy","id":"run","status":"skipped","outcome":"skipped"}]`,
			partial: true,
		},
		{
			name:  "successful deploy leaves recovery unused",
			jobs:  `[{"id":"deploy","status":"done","outcome":"success"},{"id":"rollback","status":"done","outcome":"skipped","reason":"parent_did_not_fail","on_failure_of":"deploy"}]`,
			steps: `[{"job_id":"deploy","id":"run","status":"passed","outcome":"success"}]`,
		},
		{
			name:    "ordinary skipped job still lacks proof",
			jobs:    `[{"id":"deploy","status":"done","outcome":"skipped","reason":"parent_did_not_fail"}]`,
			steps:   `[]`,
			partial: true,
		},
		{
			name:    "recovery without preview still lacks proof",
			jobs:    `[{"id":"deploy","status":"done","outcome":"success"},{"id":"rollback","status":"done","outcome":"skipped","reason":"no_dry_run_defined","on_failure_of":"deploy"}]`,
			steps:   `[{"job_id":"deploy","id":"run","status":"passed","outcome":"success"}]`,
			partial: true,
		},
		{
			name:  "fully executed preview",
			jobs:  `[{"id":"deploy","status":"done","outcome":"success"}]`,
			steps: `[{"job_id":"deploy","id":"run","status":"passed","outcome":"success"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"id":"verify-1","status":"dry_run","jobs":` + tc.jobs + `,"steps":` + tc.steps + `}`
			out, err := classifyTemplateDryRun("verify-1", raw)
			if err != nil {
				t.Fatal(err)
			}
			if out.Skipped != tc.partial || out.RanRunStep == tc.partial || tc.partial && out.SkipReason == "" {
				t.Fatalf("outcome = %+v, want partial=%t and ran=%t", out, tc.partial, !tc.partial)
			}
			dir := t.TempDir()
			recordVerification(t.Context(), dir, "dry-run-digest", "cloudrun-deploy-source", out)
			if proofRecorded(dir, "dry-run-digest") == tc.partial {
				t.Fatalf("proof recorded for partial=%t", tc.partial)
			}
		})
	}
}

func TestTemplateDryRunMissingEvidenceFails(t *testing.T) {
	for _, raw := range []string{
		`not JSON`, `{}`, `{"id":"other","status":"dry_run","jobs":[{}],"steps":[{}]}`,
		`{"id":"verify-1","status":"running","jobs":[{}],"steps":[{}]}`,
		`{"id":"verify-1","status":"dry_run","jobs":[],"steps":[]}`,
		`{"id":"verify-1","status":"dry_run","jobs":[{}]}`,
		`{"id":"verify-1","status":"dry_run","jobs":[{}],"steps":[{}]}`,
	} {
		out, err := classifyTemplateDryRun("verify-1", raw)
		if err == nil || out.RanRunStep {
			t.Fatalf("evidence %s: outcome=%+v, error=%v; want unverified", raw, out, err)
		}
	}
}
