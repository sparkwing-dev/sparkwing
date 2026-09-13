package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func TestConfigureInitNamesAnUngatedCheckout(t *testing.T) {
	info := ConfigureInit{
		ConfigDir: t.TempDir(),
		Hooks: &githooks.RepoGates{
			Repo:     "/repo",
			Declared: []string{"pre-commit"},
			Missing:  []string{"pre-commit"},
			State:    githooks.GateUninstalled,
		},
	}
	out := captureStdout(t, func() { printConfigureInitTable(info) })
	if !strings.Contains(out, "GIT HOOKS") {
		t.Fatalf("no hook verdict in:\n%s", out)
	}
	if !strings.Contains(out, "fix: sparkwing pipeline hooks install --repo /repo") {
		t.Fatalf("no repair command in:\n%s", out)
	}
}

func TestConfigureInitOffersNoRepairForAnArmedCheckout(t *testing.T) {
	info := ConfigureInit{
		ConfigDir: t.TempDir(),
		Hooks: &githooks.RepoGates{
			Repo:     "/repo",
			Declared: []string{"pre-commit"},
			Firing:   []string{"pre-commit"},
			State:    githooks.GateArmed,
		},
	}
	out := captureStdout(t, func() { printConfigureInitTable(info) })
	if !strings.Contains(out, "GIT HOOKS") {
		t.Fatalf("no hook verdict in:\n%s", out)
	}
	if strings.Contains(out, "fix:") {
		t.Fatalf("an armed checkout was offered a repair:\n%s", out)
	}
}

func TestConfigureInitNamesAProjectWhoseConfigWillNotLoad(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".sparkwing", "sparkwing.yaml")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("pipelines: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(project), "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })

	gates := surveyProjectGates()
	if gates == nil {
		t.Fatal("a project whose config will not load reported nothing at all")
	}
	if gates.State != githooks.GateBroken {
		t.Fatalf("state = %q, want %q", gates.State, githooks.GateBroken)
	}
	if gates.Gated() {
		t.Fatal("a project whose config will not load reported as gated")
	}
}

func TestConfigureInitSaysNothingAboutHooksOutsideAProject(t *testing.T) {
	out := captureStdout(t, func() { printConfigureInitTable(ConfigureInit{ConfigDir: t.TempDir()}) })
	if strings.Contains(out, "GIT HOOKS") {
		t.Fatalf("hook verdict printed with no project:\n%s", out)
	}
}

func TestConfigureInitSurveysTheCheckoutItStandsIn(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".sparkwing", "sparkwing.yaml")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(gateProject), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(filepath.Dir(project), "main.go")
	if err := os.WriteFile(entry, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	restore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })

	gates := surveyProjectGates()
	if gates == nil {
		t.Fatal("configure init surveyed no gates inside a project")
	}
	if gates.Gated() {
		t.Fatalf("a checkout with no installed hooks reported as gated: %+v", gates)
	}
	if !strings.Contains(gates.Remedy(), "sparkwing pipeline hooks install") {
		t.Fatalf("remedy = %q", gates.Remedy())
	}
}
