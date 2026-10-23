package nodehost

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
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
	run("config", "commit.gpgsign", "false")

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
	t.Chdir(dir)
	fn()
}

func TestRepoFiles_StableAcrossReruns(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx":  "export const x = 1;\n",
		"package.json": `{"name":"t"}`,
		"README.md":    "# hi",
	})
	withWorkDir(t, dir, func() {
		a := resolvedKey(t, inputs.RepoFiles())
		b := resolvedKey(t, inputs.RepoFiles())
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
		before := resolvedKey(t, inputs.RepoFiles())

		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "export const x = 2;\n",
		})

		after := resolvedKey(t, inputs.RepoFiles())
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
		fn := inputs.RepoFiles(inputs.Ignore("*.md"))
		before := resolvedKey(t, fn)

		writeAll(t, dir, map[string]string{
			"README.md": "# completely different",
		})

		after := resolvedKey(t, fn)
		if before != after {
			t.Fatalf("inputs.Ignore(*.md) should keep hash stable on README edit: %q vs %q", before, after)
		}
	})
}

func TestRepoFiles_IgnoreStillBustsOnNonIgnoredChanges(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "v1",
		"README.md":   "# hi",
	})
	withWorkDir(t, dir, func() {
		fn := inputs.RepoFiles(inputs.Ignore("*.md"))
		before := resolvedKey(t, fn)

		writeAll(t, dir, map[string]string{
			"src/foo.tsx": "v2",
		})

		after := resolvedKey(t, fn)
		if before == after {
			t.Fatalf("inputs.Ignore(*.md) must bust when non-ignored file edits: %q == %q", before, after)
		}
	})
}

func TestRepoFiles_NewFileBusts(t *testing.T) {
	dir := createTestRepository(t, map[string]string{
		"src/foo.tsx": "v1",
	})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, inputs.RepoFiles())

		writeAll(t, dir, map[string]string{"src/bar.tsx": "v1"})
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "--quiet", "-m", "add bar")

		after := resolvedKey(t, inputs.RepoFiles())
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
		key, err := resolveTestMemo(t, inputs.RepoFiles())
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
		fn := inputs.Files("src/**", "package.json")
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
		fn := inputs.Files("src/**")
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
		before := resolvedKey(t, inputs.RepoFiles())
		if before == "" {
			t.Fatal("empty hash from subdirectory workdir")
		}

		if err := os.WriteFile(filepath.Join(dir, "public", "install.sh"),
			[]byte("#!/bin/sh\necho v2"), 0o644); err != nil {
			t.Fatalf("rewrite install.sh: %v", err)
		}

		after := resolvedKey(t, inputs.RepoFiles())
		if before == after {
			t.Fatalf("hash unchanged after editing public/install.sh outside the workdir: %q",
				before)
		}
	})
}

func TestCompose_FoldsRepoFilesWithEnv(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"x.txt": "v1"})
	withWorkDir(t, dir, func() {
		fn := inputs.Compose(inputs.RepoFiles(), inputs.Env("MY_VAR"))

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
		a := resolvedKey(t, inputs.Compose(inputs.RepoFiles(), inputs.Const("v1")))
		b := resolvedKey(t, inputs.Compose(inputs.RepoFiles(), inputs.Const("v2")))
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
		a := resolvedKey(t, inputs.Tree("sibling"))
		b := resolvedKey(t, inputs.Tree("sibling"))
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
		before := resolvedKey(t, inputs.Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/a.md"), []byte("beta"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, inputs.Tree("sibling"))
		if before == after {
			t.Fatalf("Tree should bust on file edit: %q", before)
		}
	})
}

func TestTreeHashesNonUTF8Names(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames use Unicode")
	}
	for _, name := range []string{"tree/file\xff", "tree/dir\xff/file"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeAll(t, dir, map[string]string{name: "first"})
			before, err := hashTree(t.Context(), dir, "tree")
			if err != nil || before == "" {
				t.Fatalf("hash tree = %q, %v", before, err)
			}
			writeAll(t, dir, map[string]string{name: "second"})
			after, err := hashTree(t.Context(), dir, "tree")
			if err != nil || after == before {
				t.Fatalf("edited tree = %q, %v; before %q", after, err, before)
			}
		})
	}
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
		before := resolvedKey(t, inputs.Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/ignored.md"), []byte("v2"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, inputs.Tree("sibling"))
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
		key, err := resolveTestMemo(t, inputs.Tree("nonexistent-dir"))
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
		before := resolvedKey(t, inputs.Tree("sibling"))
		if err := os.WriteFile(filepath.Join(dir, "sibling/b.md"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := resolvedKey(t, inputs.Tree("sibling"))
		if before == after {
			t.Fatalf("Tree should bust when a new file appears: %q", before)
		}
	})
}

func TestTreeDoesNotFollowSymlinks(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"tree/sub/real.txt": "real"})
	outside := t.TempDir()
	writeAll(t, outside, map[string]string{"outside.txt": "first"})
	if err := os.Symlink(outside, filepath.Join(dir, "tree", "link")); err != nil {
		t.Fatal(err)
	}
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, inputs.Tree("tree"))
		writeAll(t, outside, map[string]string{"outside.txt": "second"})
		if after := resolvedKey(t, inputs.Tree("tree")); before != after {
			t.Fatalf("symlink target changed tree key: %q, %q", before, after)
		}
	})
}

func TestTreeRejectsSymlinkRoot(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"tree/sub/real.txt": "real"})
	if err := os.Symlink(filepath.Join(dir, "tree"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	withWorkDir(t, dir, func() {
		if key, err := resolveTestMemo(t, inputs.Tree("linked")); key != "" || err == nil {
			t.Fatalf("symlink root = (%q, %v)", key, err)
		}
		if key, err := resolveTestMemo(t, inputs.Tree("linked/sub")); key != "" || err == nil {
			t.Fatalf("symlink ancestor = (%q, %v)", key, err)
		}
	})
}

func TestRepoFilesHashesTrackedSymlinkWithoutFollowingTarget(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"source.txt": "source"})
	outside := t.TempDir()
	writeAll(t, outside, map[string]string{"output.txt": "first"})
	link := filepath.Join(dir, "output-link")
	if err := os.Symlink(filepath.Join(outside, "output.txt"), link); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "output-link")
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, inputs.RepoFiles())
		writeAll(t, outside, map[string]string{"output.txt": "second"})
		if after := resolvedKey(t, inputs.RepoFiles()); before != after {
			t.Fatalf("symlink target changed tracked key: %q, %q", before, after)
		}
	})
}

func TestRepoFilesFramesPathsAndContents(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"a": "x\x00b\x00y"})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, inputs.RepoFiles())
		writeAll(t, dir, map[string]string{"a": "x", "b": "y"})
		gitIn(t, dir, "add", "b")
		if after := resolvedKey(t, inputs.RepoFiles()); before == after {
			t.Fatalf("different tracked file sets share a key: %q", before)
		}
	})
}

func TestRepoFilesIncludesSymlinkType(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"a": "target"})
	withWorkDir(t, dir, func() {
		before := resolvedKey(t, inputs.RepoFiles())
		if err := os.Remove(filepath.Join(dir, "a")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target", filepath.Join(dir, "a")); err != nil {
			t.Fatal(err)
		}
		if after := resolvedKey(t, inputs.RepoFiles()); before == after {
			t.Fatalf("regular file and symlink share a key: %q", before)
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

func TestRepoFilesRejectsDirectoryComponentReplacedAfterListing(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"dir/file": "original"})
	outside := t.TempDir()
	writeAll(t, outside, map[string]string{"file": "outside"})
	environment, err := procenv.Build(procenv.Attempt, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	keep := func(path string) (bool, error) {
		if err := os.Rename(filepath.Join(dir, "dir"), filepath.Join(dir, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "dir")); err != nil {
			t.Fatal(err)
		}
		return true, nil
	}
	if key, err := hashTrackedFiles(t.Context(), dir, keep, environment); key != "" || err == nil {
		t.Fatalf("replaced directory component was followed: %q, %v", key, err)
	}
}

func TestTreeRejectsFileSwappedForSymlinkAfterListing(t *testing.T) {
	for _, inside := range []bool{false, true} {
		t.Run(fmt.Sprintf("target-inside=%v", inside), func(t *testing.T) {
			dir := t.TempDir()
			writeAll(t, dir, map[string]string{"file": "original"})
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var files []string
			if err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
				if err == nil && entry.Type().IsRegular() && !entry.IsDir() {
					files = append(files, path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			if inside {
				target = filepath.Join(dir, "target")
			}
			if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, "file")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "file")); err != nil {
				t.Fatal(err)
			}
			if key, err := hashMemoFiles(t.Context(), root, files, false); key != "" || err == nil {
				t.Fatalf("replacement symlink was followed: %q, %v", key, err)
			}
		})
	}
}

func TestTreeRejectsUnauthorizedRoots(t *testing.T) {
	dir := createTestRepository(t, map[string]string{"tree/file": "inside"})
	outside := t.TempDir()
	writeAll(t, outside, map[string]string{"file": "outside"})
	withWorkDir(t, dir, func() {
		for _, root := range []string{outside, "../" + filepath.Base(outside), "tree/../../" + filepath.Base(outside)} {
			if key, err := resolveTestMemo(t, inputs.Tree(root)); key != "" || err == nil {
				t.Fatalf("unauthorized tree root %q = %q, %v", root, key, err)
			}
		}
	})
}
