package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeRootGoMod = `module github.com/sparkwing-dev/sparkwing

go 1.26.0
`

const fakePipelinesGoMod = `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.1.0

` + selfReplaceComment + selfReplaceLine + `
`

// refuseUnpinnedGate stands in for this repo's pre-commit gate: it
// refuses a `.sparkwing/go.mod` with no local replace, which is what
// the real gate does by failing to build the pipeline module against a
// pin whose tag does not exist yet.
const refuseUnpinnedGate = `#!/bin/sh
if git show ":.sparkwing/go.mod" | grep -q 'replace github.com/sparkwing-dev/sparkwing => \.\.'; then
	exit 0
fi
echo "gate: .sparkwing module does not build without the local replace" >&2
exit 1
`

const fakeRegenAPISnapshot = `#!/bin/sh
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

// seedReleaseRepo builds a throwaway repo shaped like sparkwing (root
// module plus a nested `.sparkwing` pipelines module on a local
// replace) with a bare origin to push to, and returns its path.
func seedReleaseRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	repo := filepath.Join(base, "repo")

	gitRun(t, base, "init", "--bare", "-b", "main", origin)
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "go.mod"), fakeRootGoMod)
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n")
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.mod"), fakePipelinesGoMod)
	writeFile(t, filepath.Join(repo, ".sparkwing", "go.sum"), "")
	writeFile(t, filepath.Join(repo, ".sparkwing", "main.go"), "package main\n\nimport _ \"github.com/sparkwing-dev/sparkwing\"\n\nfunc main() {}\n")
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
	writeFile(t, filepath.Join(repo, "bin", "regen-api-snapshot.sh"), fakeRegenAPISnapshot)

	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "test")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "tag.gpgsign", "false")
	seedHooks := filepath.Join(base, "seed-hooks")
	if err := os.MkdirAll(seedHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", seedHooks)
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "seed")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "-u", "origin", "main")

	hooks := filepath.Join(base, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(refuseUnpinnedGate), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	return repo
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readPipelinesGoMod(t *testing.T, repo string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.mod"))
	if err != nil {
		t.Fatalf("read .sparkwing/go.mod: %v", err)
	}
	return string(body)
}

func TestRestoreSelfReplaceRepairsAFailedBump(t *testing.T) {
	repo := seedReleaseRepo(t)
	ctx := context.Background()

	if err := bumpSelfReplace(ctx, repo, "v0.9.9"); err == nil {
		t.Fatal("bumpSelfReplace should have failed: the gate refuses a pin with no local replace")
	}
	if strings.Contains(readPipelinesGoMod(t, repo), selfReplaceLine) {
		t.Fatal("test does not reproduce the defect: the bump left the local replace in place, so there is nothing to restore")
	}

	if err := restoreSelfReplaceIn(ctx, repo); err != nil {
		t.Fatalf("restoreSelfReplaceIn after a failed bump: %v", err)
	}
	tidy := exec.Command("go", "mod", "tidy", "-diff")
	tidy.Dir = filepath.Join(repo, ".sparkwing")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("restored pipeline module is not tidy:\n%s", out)
	}

	got := readPipelinesGoMod(t, repo)
	if !strings.Contains(got, selfReplaceLine) {
		t.Errorf(".sparkwing/go.mod does not carry the local replace after the restore:\n%s", got)
	}
	if !strings.Contains(got, sparkwingModulePath+" v0.9.9") {
		t.Errorf(".sparkwing/go.mod lost the bumped require:\n%s", got)
	}
	if dirty := gitRun(t, repo, "status", "--porcelain"); strings.TrimSpace(dirty) != "" {
		t.Errorf("working tree left dirty after the restore:\n%s", dirty)
	}
	if head := gitRun(t, repo, "log", "-1", "--pretty=%s"); !strings.Contains(head, "restore .sparkwing/ local self-replace") {
		t.Errorf("restore did not commit; HEAD subject = %q", strings.TrimSpace(head))
	}
	if local, remote := gitRun(t, repo, "rev-parse", "main"), gitRun(t, repo, "rev-parse", "origin/main"); local != remote {
		t.Error("restore did not push the repair to origin")
	}
}

func TestReleaseBumpLeavesVersionFreshnessGreen(t *testing.T) {
	repo := seedReleaseRepo(t)
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	gitRun(t, repo, "tag", "v0.99.0")
	if problem := checkScaffoldFallbackPin(context.Background(), repo); problem == "" {
		t.Fatal("freshness before release bump = green, want stale fallback")
	}

	if err := bumpSelfReplace(context.Background(), repo, "v0.99.0"); err != nil {
		t.Fatalf("bumpSelfReplace: %v", err)
	}
	if problem := checkScaffoldFallbackPin(context.Background(), repo); problem != "" {
		t.Fatalf("freshness after release bump = %q, want green", problem)
	}

	committed := gitRun(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, scaffoldFallbackRel) {
		t.Errorf("release bump commit omitted %s:\n%s", scaffoldFallbackRel, committed)
	}
	snapshotPath := scaffoldAPISnapshotRel
	if !strings.Contains(committed, snapshotPath) {
		t.Errorf("release bump commit omitted %s:\n%s", snapshotPath, committed)
	}
	contents := gitRun(t, repo, "show", "HEAD:"+scaffoldFallbackRel)
	if !strings.Contains(contents, `FallbackSDKVersion = "v0.99.0"`) {
		t.Errorf("committed fallback does not contain v0.99.0:\n%s", contents)
	}
	snapshot := gitRun(t, repo, "show", "HEAD:"+snapshotPath)
	if !strings.Contains(snapshot, `FallbackSDKVersion = "v0.99.0"`) {
		t.Errorf("committed API snapshot does not contain v0.99.0:\n%s", snapshot)
	}
}

func TestRestoreSelfReplaceIsANoopWhenTheBumpNeverRan(t *testing.T) {
	repo := seedReleaseRepo(t)
	before := gitRun(t, repo, "rev-parse", "HEAD")

	if err := restoreSelfReplaceIn(context.Background(), repo); err != nil {
		t.Fatalf("restoreSelfReplaceIn on an untouched repo: %v", err)
	}

	if after := gitRun(t, repo, "rev-parse", "HEAD"); after != before {
		t.Error("restore committed against an untouched repo; it must be a noop when the replace is already present")
	}
}
