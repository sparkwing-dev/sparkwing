package bincache

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
)

func newGitPipelineDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := newPipelineDir(t)
	cmd := testgit.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	return dir
}

func TestPipelineCacheKey_SkipsGitignoredFiles(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n*.tfplan\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "dist/binary"), "several megabytes, pretend")
	writeFile(t, filepath.Join(dir, "local.tfplan"), "provider state")

	if after := mustKey(t, dir); after != before {
		t.Fatalf("gitignored files must not affect the key: %s -> %s", before, after)
	}
}

func TestPipelineCacheKey_InvalidatesOnTrackedFileEdit(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "jobs.go"), "package main\n\nfunc extra() {}\n")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("a tracked source file must move the key, got %s twice", before)
	}
}

func TestPipelineCacheKey_InvalidatesOnGitignoreEdit(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	writeFile(t, filepath.Join(dir, "generated.go"), "package main\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\ngenerated.go\n")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("newly ignoring a hashed file must move the key, got %s twice", before)
	}
}

func TestPipelineCacheKey_HashAllFilesRestoresFullHashing(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	before, _, err := explainCacheKey(dir, nil, true)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "dist/binary"), "pretend output")

	if after, _, err := explainCacheKey(dir, nil, true); err != nil || after == before {
		t.Fatalf("hash-all-files did not include ignored files: %s: %v", after, err)
	}
}

func TestPipelineCacheKey_NonRepoDirHashesEverything(t *testing.T) {
	dir := newPipelineDir(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	writeFile(t, filepath.Join(dir, ".gitignore"), "dist/\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, "dist/binary"), "pretend output")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("outside a repository nothing is ignorable, got %s twice", before)
	}
}

func TestPipelineCacheKey_IgnoresALinkedWorktreesGitFile(t *testing.T) {
	for _, rel := range []string{".git", filepath.Join("vendored", ".git")} {
		dir := newPipelineDir(t)
		writeFile(t, filepath.Join(dir, rel), "gitdir: /depot/repo/.git/worktrees/north\n")
		before := mustKey(t, dir)

		writeFile(t, filepath.Join(dir, rel), "gitdir: /depot/repo/.git/worktrees/south\n")

		if after := mustKey(t, dir); after != before {
			t.Errorf("two worktrees of one commit must share a key with %s: %s -> %s", rel, before, after)
		}
	}
}

func TestPipelineCacheKey_InvalidatesOnGitattributesEdit(t *testing.T) {
	dir := newPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.go text\n")
	before := mustKey(t, dir)

	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.go text eol=lf\n")

	if after := mustKey(t, dir); after == before {
		t.Fatalf("a .gitattributes edit must move the key, got %s twice", before)
	}
}

func TestHashSkipsWhollyIgnoredDirectoriesButHashesTrackedFilesInThem(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "cache/\nvendored/\n")
	writeFile(t, filepath.Join(dir, "cache/lint/entry"), "cached")
	writeFile(t, filepath.Join(dir, "vendored/tracked.go"), "package vendored\n")
	writeFile(t, filepath.Join(dir, "vendored/scratch/out"), "ignored")
	add := testgit.Command("git", "add", "-f", "vendored/tracked.go")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	var seen []string
	keep := func(name string) bool { seen = append(seen, name); return true }
	files, _, err := hashable(dir, keep, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range seen {
		if name == "entry" || name == "out" {
			t.Fatalf("walk visited %s inside a wholly ignored directory", name)
		}
	}
	tracked := filepath.Join(dir, "vendored/tracked.go")
	for _, path := range files {
		if path == tracked {
			return
		}
	}
	t.Fatalf("tracked file under an ignored directory left the key: %v", files)
}

func TestHashableAgreesWithCheckIgnore(t *testing.T) {
	dir := newGitPipelineDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "logs/*\n!logs/keep.txt\n*.tmp\nbuild/\n")
	writeFile(t, filepath.Join(dir, "sub/.gitignore"), "local.json\n")
	for _, name := range []string{"logs/a.log", "logs/keep.txt", "x.tmp", "sub/x.tmp", "sub/local.json", "sub/ok.go", "build/out/bin", "tracked.tmp"} {
		writeFile(t, filepath.Join(dir, name), name)
	}
	add := testgit.Command("git", "add", "-f", "tracked.tmp")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	files, _, err := hashable(dir, allFiles, false)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, path := range files {
		kept[path] = true
	}
	all, _, err := walkHashable(dir, allFiles, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range all {
		check := testgit.Command("git", "check-ignore", "-q", path)
		check.Dir = dir
		ignored := check.Run() == nil
		if ignored == kept[path] {
			t.Errorf("%s: check-ignore ignored=%t but hashable kept=%t", path, ignored, kept[path])
		}
	}
}
