package gatescope

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalk_SkipsTheDirectoriesNoGateJudges(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.go", "internal/b.go", "testdata/c.go", "node_modules/d.go", "notes.md"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var seen []string
	if err := Walk(root, ".go", func(_, rel string) { seen = append(seen, rel) }); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := strings.Join(seen, " ")
	for _, want := range []string{"a.go", "internal/b.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("Walk saw %q, want it to reach %q", got, want)
		}
	}
	for _, skipped := range []string{"testdata", "node_modules", "notes.md"} {
		if strings.Contains(got, skipped) {
			t.Errorf("Walk saw %q; %q is outside every gate's scope", got, skipped)
		}
	}
}

func TestAddedLines_ChargesTheBranchAndItsUntrackedFiles(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "tracked.go"), "package p\n\nfunc a() {}\nfunc b() {}\n")
	runGit(t, repo, "add", "tracked.go")
	runGit(t, repo, "commit", "-m", "branch work")
	write(t, filepath.Join(repo, "untracked.go"), "package p\n\nfunc c() {}\n")
	write(t, filepath.Join(repo, "untracked.md"), "notes\n")

	added, err := AddedLines(repo, false, "main", "*.go")
	if err != nil {
		t.Fatalf("AddedLines: %v", err)
	}
	if !added["tracked.go"][3] {
		t.Errorf("added = %v; a line the branch committed is not charged to it", added)
	}
	if !added["untracked.go"][3] {
		t.Errorf("added = %v; an untracked Go file is not charged to the branch", added)
	}
	if _, ok := added["untracked.md"]; ok {
		t.Errorf("added = %v; the pathspec did not hold", added)
	}
	if _, ok := added["base.go"]; ok {
		t.Errorf("added = %v; a file only the base carries is charged to the branch", added)
	}

	if !Added(added, repo, filepath.Join(repo, "tracked.go"), 3) {
		t.Error("Added did not resolve an absolute path against the walked root")
	}
	if !Touched(added, repo, filepath.Join(repo, "untracked.go")) {
		t.Error("Touched did not name a file the scope carries")
	}
	if Touched(added, repo, filepath.Join(repo, "base.go")) {
		t.Error("Touched named a file outside the scope")
	}
}

func TestDiffFailure_NamesTheToolTheScopeAndTheEscape(t *testing.T) {
	msg := DiffFailure("sleepcheck", "origin/main", os.ErrNotExist)
	for _, want := range []string{"sleepcheck:", "origin/main", "nothing was gated", "`sleepcheck -base origin/main <root>`", "-allow-no-diff"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the diff failure does not name %q: %s", want, msg)
		}
	}
	if got := DiffFailure("commentcheck", "", os.ErrNotExist); !strings.Contains(got, "the staged diff") {
		t.Errorf("the staged-mode failure does not name its scope: %s", got)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "base.go"), "package p\n")
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "add", "base.go")
	runGit(t, dir, "commit", "-m", "base")
	runGit(t, dir, "checkout", "-b", "work")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=gatescope test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=gatescope test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
