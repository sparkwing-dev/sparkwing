package inputs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func createTestRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
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

func withWorkDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(dir)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	fn()
}

func TestRepoFiles_StableAcrossReruns(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx":  "export const x = 1;\n",
		"package.json": `{"name":"t"}`,
		"README.md":    "# hi",
	})
	withWorkDir(t, dir, func() {
		a := resolvedKey(t, RepoFiles())
		b := resolvedKey(t, RepoFiles())
		if a != b || a == "" {
			t.Fatalf("RepoFiles should be deterministic: a=%q b=%q", a, b)
		}
	})
}

func TestRepoFiles_BustsOnSourceEdit(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "export const x = 1;\n",
	})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, RepoFiles())

		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "export const x = 2;\n",
		})

		after := resolvedKey(t, RepoFiles())
		if before == after {
			t.Fatalf("RepoFiles must bust on working-tree edit: %q == %q", before, after)
		}
	})
}

func TestRepoFiles_IgnoreSkipsDocChanges(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "export const x = 1;\n",
		"README.md":   "# hi",
	})
	withWorkDir(t, dir, func() {
		fn := RepoFiles(Ignore("*.md"))
		before := resolvedKey(t, fn)

		writeAll(t, dir, map[string]string{
			"README.md": "# completely different",
		})

		after := resolvedKey(t, fn)
		if before != after {
			t.Fatalf("Ignore(*.md) should keep hash stable on README edit: %q vs %q", before, after)
		}
	})
}

func TestRepoFiles_IgnoreStillBustsOnNonIgnoredChanges(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "v1",
		"README.md":   "# hi",
	})
	withWorkDir(t, dir, func() {
		fn := RepoFiles(Ignore("*.md"))
		before := resolvedKey(t, fn)

		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "v2",
		})

		after := resolvedKey(t, fn)
		if before == after {
			t.Fatalf("Ignore(*.md) must bust when non-ignored file edits: %q == %q", before, after)
		}
	})
}

func TestRepoFiles_NewFileBusts(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "v1",
	})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, RepoFiles())

		writeAll(t, dir, map[string]string{"src/bar.tsx": "v1"})
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "--quiet", "-m", "add bar")

		after := resolvedKey(t, RepoFiles())
		if before == after {
			t.Fatalf("adding a tracked file must bust hash")
		}
	})
}

func TestRepoFiles_ReportsMissingTrackedFile(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"a.txt": "content",
		"b.txt": "content",
	})
	withWorkDir(t, dir, func() {
		if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
			t.Fatal(err)
		}
		key, err := RepoFiles()(t.Context())
		if key != "" || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing tracked file = (%q, %v)", key, err)
		}
	})
}

func TestFiles_OnlyMatchingPathsContribute(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx":  "v1",
		"package.json": "{}",
		"README.md":    "# hi",
	})
	withWorkDir(t, dir, func() {
		fn := Files("src/**", "package.json")
		before := resolvedKey(t, fn)

		writeAll(t, dir, map[string]string{"README.md": "# changed"})

		after := resolvedKey(t, fn)
		if before != after {
			t.Fatalf("Files glob should ignore README change: %q vs %q", before, after)
		}
	})
}

func TestFiles_EditWithinGlobBusts(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "v1",
		"README.md":   "# hi",
	})
	withWorkDir(t, dir, func() {
		fn := Files("src/**")
		before := resolvedKey(t, fn)
		writeAll(t, dir, map[string]string{"src/foo.tsx": "v2"})
		after := resolvedKey(t, fn)
		if before == after {
			t.Fatalf("Files glob should bust on src edit")
		}
	})
}

func TestRepoFiles_HashCoversWholeTreeFromSubdirWorkDir(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"public/install.sh":      "#!/bin/sh\necho v1",
		"src/foo.tsx":            "export const x = 1;\n",
		"sub/.sparkwing/go.mod":  "module test\n\ngo 1.21\n",
		"sub/.sparkwing/main.go": "package main\nfunc main() {}\n",
	})
	subdir := filepath.Join(dir, "sub", ".sparkwing")

	withWorkDir(t, subdir, func() {
		before := resolvedKey(t, RepoFiles())
		if before == "" {
			t.Fatal("empty hash from subdirectory workdir")
		}

		if err := os.WriteFile(filepath.Join(dir, "public", "install.sh"),
			[]byte("#!/bin/sh\necho v2"), 0o644); err != nil {
			t.Fatalf("rewrite install.sh: %v", err)
		}

		after := resolvedKey(t, RepoFiles())
		if before == after {
			t.Fatalf("hash unchanged after editing public/install.sh outside the workdir: %q",
				before)
		}
	})
}

func TestCompose_FoldsRepoFilesWithEnv(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"x.txt": "v1"})
	withWorkDir(t, dir, func() {
		fn := Compose(RepoFiles(), Env("MY_VAR"))

		t.Setenv("MY_VAR", "a")
		a := resolvedKey(t, fn)
		t.Setenv("MY_VAR", "b")
		b := resolvedKey(t, fn)
		if a == b {
			t.Fatalf("changing MY_VAR must change composed key: %q == %q", a, b)
		}
	})
}

func TestCompose_FoldsRepoFilesWithConst(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"x.txt": "v1"})
	withWorkDir(t, dir, func() {
		a := resolvedKey(t, Compose(RepoFiles(), Const("v1")))
		b := resolvedKey(t, Compose(RepoFiles(), Const("v2")))
		if a == b {
			t.Fatalf("Const bump must change composed key: %q == %q", a, b)
		}
	})
}

func TestTree_StableAcrossReruns(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"sibling/a.md":     "alpha",
		"sibling/sub/b.md": "beta",
	})
	withWorkDir(t, dir, func() {
		a := resolvedKey(t, Tree("sibling"))
		b := resolvedKey(t, Tree("sibling"))
		if a == "" || a != b {
			t.Fatalf("Tree should be stable across calls: %q vs %q", a, b)
		}
	})
}

func TestTree_BustsOnEdit(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"sibling/a.md": "alpha",
	})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/a.md"), []byte("beta"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, Tree("sibling"))
		if before == after {
			t.Fatalf("Tree should bust on file edit: %q", before)
		}
	})
}

func TestTree_BustsOnGitignoredFile(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"sibling/.gitignore": "ignored.md\n",
		"sibling/tracked.md": "real",
	})
	if err := os.WriteFile(filepath.Join(dir, "sibling/ignored.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/ignored.md"), []byte("v2"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, Tree("sibling"))
		if before == after {
			t.Fatalf("Tree must hash gitignored files: %q", before)
		}
	})
}

func TestTree_ReportsMissingRoot(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"a": "x",
	})
	withWorkDir(t, dir, func() {
		key, err := Tree("nonexistent-dir")(t.Context())
		if key != "" || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing tree = (%q, %v)", key, err)
		}
	})
}

func TestTree_NewFileBusts(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"sibling/a.md": "alpha",
	})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/b.md"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, Tree("sibling"))
		if before == after {
			t.Fatalf("Tree should bust when a new file appears: %q", before)
		}
	})
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
