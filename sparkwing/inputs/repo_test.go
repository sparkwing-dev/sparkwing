package inputs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// repoTest creates a fresh git repo in a temp dir, populates it with
// the given files, and returns the directory path. Each file is
// committed so `git ls-files` reports it. The test t.Cleanup hook
// removes the dir.
//
// Files: map of relative path -> content. Subdirectories are created
// as needed.
func repoTest(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Suppress git's "Initialized empty Git repository in ..." chatter.
		cmd.Stdout, cmd.Stderr = nil, nil
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	writeAll(t, dir, files)
	run("add", ".")
	run("commit", "--quiet", "-m", "init")
	return dir
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

// hashIn temporarily switches sparkwing.WorkDir to the test repo so
// the inputs helpers run git in the right place; restores on
// cleanup. Mirrors what the orchestrator does in production via
// SPARKWING_WORK_DIR + the runtime snapshot.
func hashIn(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(dir)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	fn()
}

// ── RepoFiles end-to-end ─────────────────────────────────────────────────

func TestRepoFiles_StableAcrossReruns(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx":  "export const x = 1;\n",
		"package.json": `{"name":"t"}`,
		"README.md":    "# hi",
	})
	hashIn(t, dir, func() {
		a := RepoFiles()(context.Background())
		b := RepoFiles()(context.Background())
		if a != b || a == "" {
			t.Fatalf("RepoFiles should be deterministic: a=%q b=%q", a, b)
		}
	})
}

func TestRepoFiles_BustsOnSourceEdit(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx": "export const x = 1;\n",
	})
	hashIn(t, dir, func() {
		before := RepoFiles()(context.Background())

		// Edit the working tree without staging — tests that we hash
		// disk content, not the git index.
		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "export const x = 2;\n",
		})

		after := RepoFiles()(context.Background())
		if before == after {
			t.Fatalf("RepoFiles must bust on working-tree edit: %q == %q", before, after)
		}
	})
}

func TestRepoFiles_IgnoreSkipsDocChanges(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx": "export const x = 1;\n",
		"README.md":   "# hi",
	})
	hashIn(t, dir, func() {
		fn := RepoFiles(Ignore("*.md"))
		before := fn(context.Background())

		writeAll(t, dir, map[string]string{
			"README.md": "# completely different",
		})

		after := fn(context.Background())
		if before != after {
			t.Fatalf("Ignore(*.md) should keep hash stable on README edit: %q vs %q", before, after)
		}
	})
}

func TestRepoFiles_IgnoreStillBustsOnNonIgnoredChanges(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx": "v1",
		"README.md":   "# hi",
	})
	hashIn(t, dir, func() {
		fn := RepoFiles(Ignore("*.md"))
		before := fn(context.Background())

		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "v2",
		})

		after := fn(context.Background())
		if before == after {
			t.Fatalf("Ignore(*.md) must bust when non-ignored file edits: %q == %q", before, after)
		}
	})
}

func TestRepoFiles_NewFileBusts(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx": "v1",
	})
	hashIn(t, dir, func() {
		before := RepoFiles()(context.Background())

		// Add a new file and commit so `git ls-files` sees it.
		writeAll(t, dir, map[string]string{"src/bar.tsx": "v1"})
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "--quiet", "-m", "add bar")

		after := RepoFiles()(context.Background())
		if before == after {
			t.Fatalf("adding a tracked file must bust hash")
		}
	})
}

// Verifies that a deleted-but-still-indexed file (`git rm` minus commit,
// or working-tree delete) doesn't crash the hash.
func TestRepoFiles_HandlesIndexTreeMismatch(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"a.txt": "content",
		"b.txt": "content",
	})
	hashIn(t, dir, func() {
		// Delete b.txt from the working tree without staging — index
		// still lists it.
		if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
			t.Fatal(err)
		}
		got := RepoFiles()(context.Background())
		if got == "" {
			t.Fatal("RepoFiles must not error on tree/index mismatch")
		}
	})
}

// ── Files (glob include) end-to-end ──────────────────────────────────────

func TestFiles_OnlyMatchingPathsContribute(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx":  "v1",
		"package.json": "{}",
		"README.md":    "# hi",
	})
	hashIn(t, dir, func() {
		fn := Files("src/**", "package.json")
		before := fn(context.Background())

		// Edit something NOT in the include list — hash should be stable.
		writeAll(t, dir, map[string]string{"README.md": "# changed"})

		after := fn(context.Background())
		if before != after {
			t.Fatalf("Files glob should ignore README change: %q vs %q", before, after)
		}
	})
}

func TestFiles_EditWithinGlobBusts(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"src/foo.tsx": "v1",
		"README.md":   "# hi",
	})
	hashIn(t, dir, func() {
		fn := Files("src/**")
		before := fn(context.Background())
		writeAll(t, dir, map[string]string{"src/foo.tsx": "v2"})
		after := fn(context.Background())
		if before == after {
			t.Fatalf("Files glob should bust on src edit")
		}
	})
}

// ── ISS-037: WorkDir = subdir must NOT silently drop tree ────────────────

// When WorkDir() points at a subdirectory of the repo (e.g. .sparkwing/
// in a v0.41.0 SDK + v0.45+ wing CLI binding where the env-var handoff
// was retired), `git ls-files` from that cwd default-scopes to the
// subdir. The hash silently dropped every file outside .sparkwing/,
// so edits to top-level tracked files (install.sh, source files,
// CHANGELOG, ...) never busted the cache. ISS-037 captures the
// real-world hit; this test pins the regression.
func TestRepoFiles_HashCoversWholeTreeFromSubdirWorkDir(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"public/install.sh":      "#!/bin/sh\necho v1",
		"src/foo.tsx":            "export const x = 1;\n",
		"sub/.sparkwing/go.mod":  "module test\n\ngo 1.21\n",
		"sub/.sparkwing/main.go": "package main\nfunc main() {}\n",
	})
	subdir := filepath.Join(dir, "sub", ".sparkwing")

	hashIn(t, subdir, func() {
		before := RepoFiles()(context.Background())
		if before == "" {
			t.Fatal("RepoFiles returned empty hash from subdir WorkDir; ls-files likely couldn't enumerate")
		}

		// Edit a file OUTSIDE the WorkDir subdirectory.
		if err := os.WriteFile(filepath.Join(dir, "public", "install.sh"),
			[]byte("#!/bin/sh\necho v2"), 0o644); err != nil {
			t.Fatalf("rewrite install.sh: %v", err)
		}

		after := RepoFiles()(context.Background())
		if before == after {
			t.Fatalf("RepoFiles must bust on edits outside WorkDir subdir; "+
				"got %q before AND after editing public/install.sh "+
				"(ISS-037: ls-files was likely cwd-scoped, hiding the file from the hash)",
				before)
		}
	})
}

// ── Compose composes ─────────────────────────────────────────────────────

func TestCompose_FoldsRepoFilesWithEnv(t *testing.T) {
	dir := repoTest(t, map[string]string{"x.txt": "v1"})
	hashIn(t, dir, func() {
		fn := Compose(RepoFiles(), Env("MY_VAR"))

		t.Setenv("MY_VAR", "a")
		a := fn(context.Background())
		t.Setenv("MY_VAR", "b")
		b := fn(context.Background())
		if a == b {
			t.Fatalf("changing MY_VAR must change composed key: %q == %q", a, b)
		}
	})
}

func TestCompose_FoldsRepoFilesWithConst(t *testing.T) {
	dir := repoTest(t, map[string]string{"x.txt": "v1"})
	hashIn(t, dir, func() {
		// Bumping Const value must invalidate even when files are stable.
		a := Compose(RepoFiles(), Const("v1"))(context.Background())
		b := Compose(RepoFiles(), Const("v2"))(context.Background())
		if a == b {
			t.Fatalf("Const bump must change composed key: %q == %q", a, b)
		}
	})
}

// ── Tree end-to-end ──────────────────────────────────────────────────────

func TestTree_StableAcrossReruns(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"sibling/a.md":     "alpha",
		"sibling/sub/b.md": "beta",
	})
	hashIn(t, dir, func() {
		a := Tree("sibling")(context.Background())
		b := Tree("sibling")(context.Background())
		if a == "" || a != b {
			t.Fatalf("Tree should be stable across calls: %q vs %q", a, b)
		}
	})
}

func TestTree_BustsOnEdit(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"sibling/a.md": "alpha",
	})
	hashIn(t, dir, func() {
		before := Tree("sibling")(context.Background())
		if err := os.WriteFile(filepath.Join(dir, "sibling/a.md"), []byte("beta"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := Tree("sibling")(context.Background())
		if before == after {
			t.Fatalf("Tree should bust on file edit: %q", before)
		}
	})
}

func TestTree_BustsOnGitignoredFile(t *testing.T) {
	// The whole point of Tree: it sees gitignored files that
	// RepoFiles skips. A .gitignored file inside the watched dir
	// must contribute to the hash, otherwise build artifacts the
	// caller deliberately put outside git would produce stale cache
	// hits.
	dir := repoTest(t, map[string]string{
		"sibling/.gitignore": "ignored.md\n",
		"sibling/tracked.md": "real",
	})
	if err := os.WriteFile(filepath.Join(dir, "sibling/ignored.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashIn(t, dir, func() {
		before := Tree("sibling")(context.Background())
		if err := os.WriteFile(filepath.Join(dir, "sibling/ignored.md"), []byte("v2"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := Tree("sibling")(context.Background())
		if before == after {
			t.Fatalf("Tree must hash gitignored files: %q", before)
		}
	})
}

func TestTree_MissingRootReturnsEmpty(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"a": "x",
	})
	hashIn(t, dir, func() {
		k := Tree("nonexistent-dir")(context.Background())
		if k != "" {
			t.Fatalf("Tree on missing root must return empty key (no cache); got %q", k)
		}
	})
}

func TestTree_NewFileBusts(t *testing.T) {
	dir := repoTest(t, map[string]string{
		"sibling/a.md": "alpha",
	})
	hashIn(t, dir, func() {
		before := Tree("sibling")(context.Background())
		if err := os.WriteFile(filepath.Join(dir, "sibling/b.md"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := Tree("sibling")(context.Background())
		if before == after {
			t.Fatalf("Tree should bust when a new file appears: %q", before)
		}
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
