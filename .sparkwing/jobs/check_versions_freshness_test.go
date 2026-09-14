package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScaffoldFallbackProblem(t *testing.T) {
	cases := []struct {
		name   string
		pinned string
		latest string
		wantOK bool
	}{
		{"current", "v0.15.3", "v0.15.3", true},
		{"ahead", "v0.15.4", "v0.15.3", true},
		{"behind", "v0.8.1", "v0.15.3", false},
		{"behind by patch", "v0.15.2", "v0.15.3", false},
		{"invalid pin", "", "v0.15.3", false},
		{"non-semver pin", "(devel)", "v0.15.3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scaffoldFallbackProblem(c.pinned, c.latest)
			if c.wantOK && got != "" {
				t.Errorf("scaffoldFallbackProblem(%q, %q) = %q, want no problem", c.pinned, c.latest, got)
			}
			if !c.wantOK && got == "" {
				t.Errorf("scaffoldFallbackProblem(%q, %q) reported no problem, want one", c.pinned, c.latest)
			}
		})
	}
}

func TestBumpFallbackSDKVersionFile(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		version    string
		wantErr    bool
		wantResult string
	}{
		{
			name:       "bumps current version to newer",
			src:        `const FallbackSDKVersion = "v0.18.0"` + "\n",
			version:    "v0.19.0",
			wantResult: `const FallbackSDKVersion = "v0.19.0"` + "\n",
		},
		{
			name:       "bumps to same version (idempotent)",
			src:        `const FallbackSDKVersion = "v0.19.0"` + "\n",
			version:    "v0.19.0",
			wantResult: `const FallbackSDKVersion = "v0.19.0"` + "\n",
		},
		{
			name:    "missing pattern returns error",
			src:     "package scaffold\n",
			version: "v0.19.0",
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			scaffoldDir := filepath.Join(dir, "pkg", "scaffold")
			if err := os.MkdirAll(scaffoldDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(scaffoldDir, "version.go")
			if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}
			err := bumpFallbackSDKVersionFile(dir, c.version)
			if c.wantErr {
				if err == nil {
					t.Error("bumpFallbackSDKVersionFile() returned nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("bumpFallbackSDKVersionFile() error: %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != c.wantResult {
				t.Errorf("version.go content = %q, want %q", string(got), c.wantResult)
			}
		})
	}
}

func TestCommitSparkwingPinBump(t *testing.T) {
	mustGit := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	initRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		mustGit(t, dir, "init")
		mustGit(t, dir, "config", "user.email", "test@example.com")
		mustGit(t, dir, "config", "user.name", "Test")
		mustGit(t, dir, "config", "commit.gpgsign", "false")
		mustGit(t, dir, "config", "tag.gpgsign", "false")
		mustGit(t, dir, "config", "core.hooksPath", t.TempDir())
		return dir
	}

	createBumpFiles := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "pkg", "scaffold"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(scaffoldAPISnapshotRel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(kubernetesE2EPipelineModuleRel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "pkg", "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		for path, content := range map[string]string{
			"CHANGELOG.md": fixtureChangelog,
			filepath.Join("pkg", "docs", "changelog.md"):                   fixtureChangelog,
			filepath.Join("pkg", "scaffold", "version.go"):                 "v0.19.0",
			filepath.Join(".sparkwing", "go.mod"):                          "module test",
			filepath.Join(".sparkwing", "go.sum"):                          "",
			filepath.FromSlash(scaffoldAPISnapshotRel):                     "v0.19.0",
			filepath.FromSlash(kubernetesE2EPipelineModuleRel + "/go.mod"): "module fixture",
			filepath.FromSlash(kubernetesE2EPipelineModuleRel + "/go.sum"): "",
		} {
			if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("creates commit with correct message and staged files", func(t *testing.T) {
		dir := initRepo(t)
		createBumpFiles(t, dir)

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err != nil {
			t.Fatalf("commitSparkwingPinBump() error: %v", err)
		}

		subject, err := captureGit(context.Background(), dir, "log", "--format=%s", "-1")
		if err != nil {
			t.Fatalf("git log: %v", err)
		}
		if got := strings.TrimSpace(subject); got != "chore: bump sparkwing pin to v0.19.0" {
			t.Errorf("commit subject = %q, want %q", got, "chore: bump sparkwing pin to v0.19.0")
		}

		filesOut, err := captureGit(context.Background(), dir, "show", "--name-only", "--format=", "HEAD")
		if err != nil {
			t.Fatalf("git show: %v", err)
		}
		for _, f := range sparkwingPinArtifacts {
			if !strings.Contains(filesOut, f) {
				t.Errorf("committed files: %q not found in output %q", f, filesOut)
			}
		}
	})

	t.Run("returns error when staged paths do not exist", func(t *testing.T) {
		dir := initRepo(t)

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err == nil {
			t.Error("commitSparkwingPinBump() returned nil, want error")
		}
	})

	t.Run("returns error when nothing is staged for commit", func(t *testing.T) {
		dir := initRepo(t)
		createBumpFiles(t, dir)

		mustGit(t, dir, append([]string{"add", "--"}, sparkwingPinArtifacts...)...)
		mustGit(t, dir, "commit", "-m", "initial")

		if err := commitSparkwingPinBump(context.Background(), dir, "v0.19.0"); err == nil {
			t.Error("commitSparkwingPinBump() returned nil, want error when nothing to commit")
		}
	})
}

func TestSparkwingPinArtifactsIncludeKubernetesE2EFixture(t *testing.T) {
	want := []string{
		"testdata/k8s-e2e/repo/.sparkwing/go.mod",
		"testdata/k8s-e2e/repo/.sparkwing/go.sum",
	}
	for _, path := range want {
		if !slices.Contains(sparkwingPinArtifacts, path) {
			t.Errorf("sparkwingPinArtifacts excludes %q", path)
		}
	}
}

func TestReleaseVersionArtifactsAlignedDetectsFixtureOnlyDrift(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	module := func(version string) string {
		return "module example.invalid/pipelines\n\ngo 1.26.0\n\nrequire " + sdkModulePath + " " + version + "\n"
	}
	write(scaffoldFallbackRel, `const FallbackSDKVersion = "v0.38.2"`)
	write(scaffoldAPISnapshotRel, `const FallbackSDKVersion = "v0.38.2"`)
	write(".sparkwing/go.mod", module("v0.38.2"))
	write(kubernetesE2EPipelineModuleRel+"/go.mod", module("v0.38.1"))

	_, aligned, err := coherentReleaseVersionArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if aligned {
		t.Fatal("fixture-only drift reported aligned")
	}
	write(kubernetesE2EPipelineModuleRel+"/go.mod", module("v0.38.2"))
	pinned, aligned, err := coherentReleaseVersionArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !aligned {
		t.Fatal("coherent release artifacts reported drift")
	}
	if pinned != "v0.38.2" {
		t.Fatalf("coherent pin = %q, want v0.38.2", pinned)
	}
}

func TestAutoBumpSparkwingPinPreservesCoherentAheadArtifacts(t *testing.T) {
	repo := seedReleaseRepo(t)
	const ahead = "v0.2.0"

	for _, rel := range []string{".sparkwing/go.mod", kubernetesE2EPipelineModuleRel + "/go.mod"} {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, strings.ReplaceAll(string(body), "v0.1.0", ahead))
	}
	writeFile(t, filepath.Join(repo, filepath.FromSlash(scaffoldFallbackRel)), "package scaffold\n\nconst FallbackSDKVersion = \""+ahead+"\"\n")
	writeFile(t, filepath.Join(repo, filepath.FromSlash(scaffoldAPISnapshotRel)), "# pkg/scaffold\n\nconst FallbackSDKVersion = \""+ahead+"\"\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "seed coherent ahead artifacts")
	gitRun(t, repo, "tag", "v0.1.0")
	gitRun(t, repo, "push", "origin", "v0.1.0")
	before := gitRun(t, repo, "rev-parse", "HEAD")

	bumped, err := autoBumpSparkwingPinIfStale(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if bumped != "" {
		t.Fatalf("auto bump returned %q, want no downgrade", bumped)
	}
	if after := gitRun(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatal("auto bump committed over coherent ahead artifacts")
	}
	if dirty := strings.TrimSpace(gitRun(t, repo, "status", "--porcelain")); dirty != "" {
		t.Fatalf("auto bump changed coherent ahead artifacts:\n%s", dirty)
	}
}

func TestAutoBumpSparkwingPinIfStale_RollsBackVersionFileOnPartialFailure(t *testing.T) {
	dir := t.TempDir()

	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	mustGit("init")
	mustGit("config", "user.email", "test@example.com")
	mustGit("config", "user.name", "Test")
	mustGit("config", "commit.gpgsign", "false")
	mustGit("config", "tag.gpgsign", "false")
	mustGit("config", "core.hooksPath", t.TempDir())
	placeholder := filepath.Join(dir, ".keep")
	if err := os.WriteFile(placeholder, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", ".keep")
	mustGit("commit", "-m", "init")
	mustGit("tag", "v0.99.0")

	scaffoldDir := filepath.Join(dir, "pkg", "scaffold")
	if err := os.MkdirAll(scaffoldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	versionFile := filepath.Join(scaffoldDir, "version.go")
	original := `const FallbackSDKVersion = "v0.18.0"` + "\n"
	if err := os.WriteFile(versionFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := autoBumpSparkwingPinIfStale(context.Background(), dir)
	if err == nil {
		t.Fatal("autoBumpSparkwingPinIfStale() returned nil, want error")
	}

	got, readErr := os.ReadFile(versionFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Errorf("version.go after partial failure = %q, want original %q (rollback failed)", string(got), original)
	}
}

func TestAutoBumpSparkwingPinIfStaleRestoresIndexAfterCommitFailure(t *testing.T) {
	dir := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	mustGit("init")
	mustGit("config", "user.email", "test@example.com")
	mustGit("config", "user.name", "Test")
	mustGit("config", "commit.gpgsign", "false")
	mustGit("config", "tag.gpgsign", "false")
	mustGit("config", "core.hooksPath", t.TempDir())
	for path, body := range map[string]string{
		"go.mod":                 "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n",
		"doc.go":                 "package sparkwing\n",
		".sparkwing/go.mod":      fixturePipelinesGoMod,
		".sparkwing/go.sum":      "",
		".sparkwing/pipeline.go": "package pipelines\n\nimport _ \"github.com/sparkwing-dev/sparkwing\"\n",
		scaffoldFallbackRel:      "package scaffold\n\nconst FallbackSDKVersion = \"v0.18.0\"\n",
		filepath.FromSlash(scaffoldAPISnapshotRel): "# pkg/scaffold\n\nconst FallbackSDKVersion = \"v0.18.0\"\n",
		"bin/regen-api-snapshot.sh":                fixtureRegenAPISnapshot,
	} {
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit("add", ".")
	mustGit("commit", "-m", "seed")
	mustGit("tag", "v0.99.0")
	unrelated := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("keep me staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "unrelated.txt")
	if _, err := autoBumpSparkwingPinIfStale(context.Background(), dir); err == nil {
		t.Fatal("auto bump accepted unrelated staged work")
	}
	if status := strings.TrimSpace(captureGitOutput(t, dir, "status", "--porcelain")); status != "A  unrelated.txt" {
		t.Fatalf("auto bump changed unrelated staged work: %q", status)
	}
	mustGit("reset", "--", "unrelated.txt")
	if err := os.Remove(unrelated); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit("config", "core.hooksPath", hooks)

	if _, err := autoBumpSparkwingPinIfStale(context.Background(), dir); err == nil {
		t.Fatal("auto bump succeeded despite refusing pre-commit hook")
	}
	if status := strings.TrimSpace(captureGitOutput(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("failed auto bump left worktree or index dirty:\n%s", status)
	}
}

func TestAutoBumpSparkwingPinIfStaleRegeneratesAPISnapshot(t *testing.T) {
	repo := seedReleaseRepo(t)
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	gitRun(t, repo, "tag", "v0.99.0")
	// safety: the pin catches up to a tag the trunk has moved past. A tag still
	// pointing at HEAD is the release being verified, which nothing can pin.
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n\n// past the tag\n")
	gitRun(t, repo, "add", "doc.go")
	gitRun(t, repo, "commit", "-m", "move past the tag")

	bumped, err := autoBumpSparkwingPinIfStale(context.Background(), repo)
	if err != nil {
		t.Fatalf("autoBumpSparkwingPinIfStale: %v", err)
	}
	if bumped != "v0.99.0" {
		t.Fatalf("autoBumpSparkwingPinIfStale = %q, want v0.99.0", bumped)
	}

	snapshotPath := scaffoldAPISnapshotRel
	committed := gitRun(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, snapshotPath) {
		t.Errorf("auto-bump commit omitted %s:\n%s", snapshotPath, committed)
	}
	snapshot := gitRun(t, repo, "show", "HEAD:"+snapshotPath)
	if !strings.Contains(snapshot, `FallbackSDKVersion = "v0.99.0"`) {
		t.Errorf("committed API snapshot does not contain v0.99.0:\n%s", snapshot)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.Dir(scaffoldAPISnapshotRel), "pkg_other.txt")); !os.IsNotExist(err) {
		t.Errorf("auto-bump copied unrelated generated API snapshot: %v", err)
	}
}

func captureGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

const fixtureRootGoMod = `module github.com/sparkwing-dev/sparkwing

go 1.26.0
`

const fixturePipelinesGoMod = `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.1.0

replace github.com/sparkwing-dev/sparkwing => ..
`

const fixtureKubernetesE2EPipelinesGoMod = `module sparkwing-k8s-e2e-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.1.0

replace github.com/sparkwing-dev/sparkwing => ../../../..
`

const fixtureSyncDocs = `#!/bin/sh
set -eu
mkdir -p pkg/docs
cp CHANGELOG.md pkg/docs/changelog.md
`

const fixtureChangelog = `# Changelog

## [Unreleased]

### Changed

- **cache:** an entry the fixture already carried.
`

const fixtureRegenAPISnapshot = `#!/bin/sh
set -eu
version=$(sed -n 's/.*FallbackSDKVersion = "\(v[^"]*\)".*/\1/p' pkg/scaffold/version.go)
out=${1:-.apidiff}
mkdir -p "$out"
printf '# pkg/scaffold\n\nconst FallbackSDKVersion = "%s"\n' "$version" > "$out/pkg_scaffold.txt"
printf '# pkg/other\n' > "$out/pkg_other.txt"
`

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func seedReleaseRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	repo := filepath.Join(base, "repo")

	gitRun(t, base, "init", "--bare", "-b", "main", origin)
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "go.mod"), fixtureRootGoMod)
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n")
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.mod"), fixturePipelinesGoMod)
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.sum"), "")
	writeFile(t, filepath.Join(repo, ".sparkwing", "main.go"), "package main\n\nimport _ \"github.com/sparkwing-dev/sparkwing\"\n\nfunc main() {}\n")
	fixtureModuleDir := filepath.Join(repo, filepath.FromSlash(kubernetesE2EPipelineModuleRel))
	if err := os.MkdirAll(fixtureModuleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixtureModuleDir, "go.mod"), fixtureKubernetesE2EPipelinesGoMod)
	writeFile(t, filepath.Join(fixtureModuleDir, "go.sum"), "")
	writeFile(t, filepath.Join(fixtureModuleDir, "main.go"), "package main\n\nimport _ \"github.com/sparkwing-dev/sparkwing\"\n\nfunc main() {}\n")
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "scaffold"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(scaffoldAPISnapshotRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, filepath.FromSlash(scaffoldFallbackRel)), "package scaffold\n\nconst FallbackSDKVersion = \"v0.1.0\"\n")
	writeFile(t, filepath.Join(repo, filepath.FromSlash(scaffoldAPISnapshotRel)), "# pkg/scaffold\n\nconst FallbackSDKVersion = \"v0.1.0\"\n")
	writeFile(t, filepath.Join(repo, "bin", "regen-api-snapshot.sh"), fixtureRegenAPISnapshot)
	writeFile(t, filepath.Join(repo, "bin", "sync-docs.sh"), fixtureSyncDocs)
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), fixtureChangelog)
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "pkg", "docs", "changelog.md"), fixtureChangelog)

	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "test")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "tag.gpgsign", "false")
	gitRun(t, repo, "config", "core.hooksPath", t.TempDir())
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "seed")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "-u", "origin", "main")
	return repo
}

const fixturePhantomVersion = "v0.52.0"

const fixturePinnedVersion = "v0.1.0"

func TestCheckVersionsFreshnessIgnoresRetractedProxyVersions(t *testing.T) {
	cases := []struct {
		name        string
		retract     bool
		wantProblem bool
	}{
		{name: "a retracted version is not an upgrade", retract: true},
		{name: "a version the repository stands behind is an upgrade", retract: false, wantProblem: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := seedProxyPinnedRepo(t, c.retract)

			err := CheckVersionsFreshness(context.Background(), repo)

			if c.wantProblem {
				if err == nil {
					t.Fatalf("CheckVersionsFreshness reported no problem, want %s offered as an upgrade", fixturePhantomVersion)
				}
				if !strings.Contains(err.Error(), fixturePhantomVersion) {
					t.Fatalf("CheckVersionsFreshness error = %v, want it to name %s", err, fixturePhantomVersion)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckVersionsFreshness: %v", err)
			}
		})
	}
}

func TestRetractedReleasesExcludesOnlyTheOwningModule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module "+sdkModulePath+`

go 1.26.0

retract (
	[v1.0.0, v1.6.1]
	v0.52.0
)
`)
	retracted, err := repoRetractedReleases(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		module  string
		version string
		want    bool
	}{
		{sdkModulePath, "v0.52.0", true},
		{sdkModulePath, "v1.3.0", true},
		{sdkModulePath, "v1.6.1", true},
		{sdkModulePath, "v0.50.5", false},
		{sdkModulePath, "v1.7.0", false},
		{"github.com/sparkwing-dev/sparks-core/templates", "v0.52.0", false},
	}
	for _, c := range cases {
		if got := retracted.excludes(c.module, c.version); got != c.want {
			t.Errorf("excludes(%q, %q) = %v, want %v", c.module, c.version, got, c.want)
		}
	}
}

func seedProxyPinnedRepo(t *testing.T, retract bool) string {
	t.Helper()
	base := t.TempDir()
	proxy := filepath.Join(base, "proxy", filepath.FromSlash(sdkModulePath), "@v")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(proxy, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proxy, "list"), fixturePinnedVersion+"\n"+fixturePhantomVersion+"\n")
	for _, version := range []string{fixturePinnedVersion, fixturePhantomVersion} {
		writeFile(t, filepath.Join(proxy, version+".info"), `{"Version":"`+version+`","Time":"2026-01-01T00:00:00Z"}`)
		writeFile(t, filepath.Join(proxy, version+".mod"), "module "+sdkModulePath+"\n\ngo 1.26.0\n")
	}

	for _, dir := range []string{".sparkwing", filepath.Join("pkg", "scaffold")} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := "module " + sdkModulePath + "\n\ngo 1.26.0\n"
	if retract {
		root += "\nretract " + fixturePhantomVersion + "\n"
	}
	writeFile(t, filepath.Join(repo, "go.mod"), root)
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n")
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.mod"),
		"module sparkwing-pipelines\n\ngo 1.26.0\n\nrequire "+sdkModulePath+" "+fixturePinnedVersion+"\n")
	writeFile(t, filepath.Join(repo, filepath.FromSlash(scaffoldFallbackRel)),
		"package scaffold\n\nconst FallbackSDKVersion = \""+fixturePinnedVersion+"\"\n")

	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "test")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "tag.gpgsign", "false")
	gitRun(t, repo, "config", "core.hooksPath", t.TempDir())
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "seed")
	gitRun(t, repo, "tag", fixturePinnedVersion)

	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(filepath.Join(base, "proxy")))
	// safety: the fixture serves invented metadata under the real module path, so
	// a shared module cache would keep a stub that fails every later checksum.
	t.Setenv("GOMODCACHE", filepath.Join(base, "modcache"))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	return repo
}
