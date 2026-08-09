package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

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
		if err := normalizeVerifyModulePath(dir, "example"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(data))
	}
	if got[0] != got[1] || !strings.Contains(got[0], "module example.com/sparkwing/verify/example/pipelines") {
		t.Fatalf("normalized modules differ or are not stable: %q, %q", got[0], got[1])
	}
}

func TestTemplateRunLeaseTokens_AreClearedAtIsolatedDaemonBoundary(t *testing.T) {
	env := templateRunAdmissionEnv(t.TempDir())
	for _, name := range []string{wingwire.LeaseTokenEnv, wingwire.ChildLeaseTokenEnv} {
		if got := env[name]; got != "" {
			t.Fatalf("%s = %q, want empty", name, got)
		}
	}
}

func TestSeedFixture_RejectsUnknown(t *testing.T) {
	if err := seedFixture(t.TempDir(), "rust-crate"); err == nil {
		t.Fatal("expected error for unknown fixture")
	}
}

// TestGoModuleFixture_HasCoverableStatements guards the coverage-gated
// template: the fixture's coverprofile must report nonzero total
// statements (a profile with only the "mode:" header means the gate
// errors with "zero total statements").
func TestGoModuleFixture_HasCoverableStatements(t *testing.T) {
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

func TestWriteMaskedSecret_LandsInScratchDotenv(t *testing.T) {
	home := t.TempDir()
	if err := writeMaskedSecret(home, "DATABASE_URL", "postgres://x"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".config", "sparkwing", "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "DATABASE_URL=postgres://x") {
		t.Fatalf("secret not written: %q", body)
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
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	dir := t.TempDir()
	if err := seedNodeModule(dir); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ci"}, {"test"}, {"run", "lint"}} {
		cmd := exec.Command("npm", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm %v: %v\n%s", args, err, out)
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
