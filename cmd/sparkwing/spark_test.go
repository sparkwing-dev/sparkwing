package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSparkFixture materializes a library tree from relative path ->
// content and returns its root.
func writeSparkFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// joinProblems flattens lint problems for substring assertions.
func joinProblems(problems []string) string {
	return strings.Join(problems, "\n")
}

func TestSparksLintAcceptsModulesManifest(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{
		"spark.json": `{
  "name": "my-sparks",
  "description": "monorepo of pipeline libraries",
  "author": "tester",
  "modules": [
    {"path": "docker", "module": "github.com/acme/my-sparks/docker", "description": "docker helpers"},
    {"path": "kube", "module": "github.com/acme/my-sparks/kube", "description": "kube helpers", "stability": "beta"}
  ]
}`,
		"docker/go.mod": "module github.com/acme/my-sparks/docker\n\ngo 1.26.0\n",
		"kube/go.mod":   "module github.com/acme/my-sparks/kube\n\ngo 1.26.0\n",
	})
	var err error
	out := captureStdout(t, func() { err = runSparksLint([]string{dir}) })
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if !strings.Contains(out, "ok:") || !strings.Contains(out, "2 modules") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestSparksLintAcceptsPackagesManifest(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{
		"spark.json": `{
  "name": "my-sparks",
  "description": "one module, many packages",
  "author": "tester",
  "packages": [
    {"path": "docker", "description": "docker helpers"},
    {"path": "gitops", "description": "gitops helpers"}
  ]
}`,
		"go.mod":         "module github.com/acme/my-sparks\n\ngo 1.26.0\n",
		"docker/doc.go":  "package docker\n",
		"gitops/doc.go":  "package gitops\n",
		"unrelated/x.go": "package unrelated\n",
	})
	var err error
	out := captureStdout(t, func() { err = runSparksLint([]string{"--path", dir}) })
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if !strings.Contains(out, "2 packages") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestSparksLintRejectsPathFlagWithPositional(t *testing.T) {
	dir := t.TempDir()
	err := runSparksLint([]string{"--path", dir, dir})
	if err == nil || !strings.Contains(err.Error(), "both given") {
		t.Fatalf("want conflict error, got %v", err)
	}
}

func TestSparksLintModuleFieldRequired(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"docker/go.mod": "module github.com/acme/my-sparks/docker\n"})
	problems := lintSparkEntries(dir, "modules", []sparkManifestEntry{
		{Path: "docker", Description: "docker helpers"},
	})
	if got := joinProblems(problems); !strings.Contains(got, "modules[0] (docker): 'module' is required") {
		t.Fatalf("want missing-module problem, got %q", got)
	}
}

func TestSparksLintModuleMustMatchGoMod(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{
		"docker/go.mod": "module github.com/acme/my-sparks/docker\n\ngo 1.26.0\n",
	})
	problems := lintSparkEntries(dir, "modules", []sparkManifestEntry{
		{Path: "docker", Module: "github.com/acme/my-sparks/dockerx", Description: "docker helpers"},
	})
	got := joinProblems(problems)
	if !strings.Contains(got, `'module' is "github.com/acme/my-sparks/dockerx"`) ||
		!strings.Contains(got, `declares "github.com/acme/my-sparks/docker"`) {
		t.Fatalf("want module mismatch problem, got %q", got)
	}
}

func TestSparksLintModuleWithoutGoModIsAccepted(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"docker/doc.go": "package docker\n"})
	problems := lintSparkEntries(dir, "modules", []sparkManifestEntry{
		{Path: "docker", Module: "github.com/acme/my-sparks/docker", Description: "docker helpers"},
	})
	if len(problems) != 0 {
		t.Fatalf("want no problems, got %q", joinProblems(problems))
	}
}

func TestSparksLintEntryPathMustExist(t *testing.T) {
	dir := t.TempDir()
	problems := lintSparkEntries(dir, "modules", []sparkManifestEntry{
		{Path: "ghost", Module: "github.com/acme/my-sparks/ghost", Description: "not there"},
	})
	if got := joinProblems(problems); !strings.Contains(got, "modules[0] (ghost): directory") ||
		!strings.Contains(got, "does not exist") {
		t.Fatalf("want missing-directory problem, got %q", got)
	}
}

func TestSparksLintRejectsDuplicateEntryPaths(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"docker/doc.go": "package docker\n"})
	problems := lintSparkEntries(dir, "packages", []sparkManifestEntry{
		{Path: "docker", Description: "one"},
		{Path: "docker", Description: "two"},
	})
	if got := joinProblems(problems); !strings.Contains(got, "packages[1] (docker): duplicate path; first seen at packages[0]") {
		t.Fatalf("want duplicate-path problem, got %q", got)
	}
}

func TestSparksLintRejectsModuleFieldInPackagesEntry(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"docker/doc.go": "package docker\n"})
	problems := lintSparkEntries(dir, "packages", []sparkManifestEntry{
		{Path: "docker", Module: "github.com/acme/my-sparks/docker", Description: "docker helpers"},
	})
	if got := joinProblems(problems); !strings.Contains(got, "'module' belongs to a 'modules' entry") {
		t.Fatalf("want stray-module problem, got %q", got)
	}
}

func TestSparkManifestShape(t *testing.T) {
	pkgs := []sparkManifestEntry{{Path: "docker", Description: "d"}}
	mods := []sparkManifestEntry{{Path: "docker", Module: "m", Description: "d"}}
	cases := []struct {
		name        string
		manifest    sparkManifest
		wantField   string
		wantEntries int
		wantProblem string
	}{
		{"packages only", sparkManifest{Packages: pkgs}, "packages", 1, ""},
		{"modules only", sparkManifest{Modules: mods}, "modules", 1, ""},
		{"both", sparkManifest{Packages: pkgs, Modules: mods}, "packages", 0, "declares both 'packages' and 'modules'"},
		{"neither", sparkManifest{}, "packages", 0, "exactly one of 'packages'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			field, entries, problem := sparkManifestShape(tc.manifest)
			if field != tc.wantField || len(entries) != tc.wantEntries {
				t.Errorf("got field %q with %d entries, want %q with %d", field, len(entries), tc.wantField, tc.wantEntries)
			}
			if tc.wantProblem == "" && problem != "" {
				t.Errorf("unexpected problem: %q", problem)
			}
			if tc.wantProblem != "" && !strings.Contains(problem, tc.wantProblem) {
				t.Errorf("problem %q does not mention %q", problem, tc.wantProblem)
			}
		})
	}
}
