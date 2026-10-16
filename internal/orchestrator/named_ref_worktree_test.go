package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func commitFile(t *testing.T, repo, name, content string) Commit {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "--quiet", "-m", "change "+name)
	return headCommit(t, repo)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func namedWorktree(t *testing.T, p paths.Paths, repo, name string) NamedRefWorktree {
	t.Helper()
	w, err := ResolveNamedRefWorktree(t.Context(), p, repo, name)
	if err != nil {
		t.Fatalf("ResolveNamedRefWorktree: %v", err)
	}
	return w
}

func namedTrigger(id string, w NamedRefWorktree, commit Commit) *store.Trigger {
	return &store.Trigger{ID: id, TriggerEnv: map[string]string{
		SubmitRepoDirKey:        w.Dir,
		NamedRefWorktreeRepoKey: w.Repo,
		RefWorktreeRevKey:       string(commit),
	}}
}

func held(t *testing.T, w NamedRefWorktree) bool {
	t.Helper()
	file, free, err := tryHoldNamedRefWorktree(w)
	if err != nil {
		t.Fatalf("probe hold: %v", err)
	}
	if free {
		if err := ReleaseRefWorktree(file); err != nil {
			t.Fatal(err)
		}
	}
	return !free
}

func TestNamedRefWorktreeRunsNeverOverlapAndEachReadsItsCommit(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	logger := slog.New(slog.DiscardHandler)

	synctest.Test(t, func(t *testing.T) {
		_, releaseFirst, err := holdNamedRefWorktreeFor(t.Context(), p, namedTrigger("run-1", w, first), logger)
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
		if !held(t, w) {
			t.Fatal("the first run executes without holding its worktree")
		}

		read := make(chan string, 1)
		go func() {
			_, releaseSecond, err := holdNamedRefWorktreeFor(t.Context(), p, namedTrigger("run-2", w, second), logger)
			if err != nil {
				read <- "error: " + err.Error()
				return
			}
			defer releaseSecond()
			raw, err := os.ReadFile(filepath.Join(w.Dir, "version.txt"))
			if err != nil {
				read <- "error: " + err.Error()
				return
			}
			read <- string(raw)
		}()

		// safety: Wait returns only once the second run has tried the worktree and
		// is parked on its poll, so a run that skipped the lease has already read.
		synctest.Wait()
		select {
		case got := <-read:
			t.Fatalf("second run took the worktree while the first held it (read %q)", got)
		default:
		}
		if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "first\n" {
			t.Fatalf("first run reads %q, want its own commit's file", got)
		}

		releaseFirst()
		if got := <-read; got != "second\n" {
			t.Fatalf("second run reads %q, want its own commit's file", got)
		}
	})
}

func TestNamedRefWorktreeSwitchKeepsIgnoredOutputAndUnchangedFiles(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	commitFile(t, repo, ".gitignore", "build/\n.sparkwing/overlay.mod\ngo.work\ngo.work.sum\n")
	commitFile(t, repo, "shared.txt", "shared\n")
	commitFile(t, repo, "edited.txt", "pristine\n")
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")
	bare := filepath.Join(t.TempDir(), "origin.git")
	runGitFixture(t, t.TempDir(), "clone", "--bare", "--quiet", repo, bare)
	for name, origin := range map[string]string{"checkout": repo, "bare clone": bare} {
		t.Run(name, func(t *testing.T) {
			p := paths.Paths{Root: t.TempDir()}
			w := namedWorktree(t, p, origin, "checks")
			ctx := context.Background()

			if err := checkoutNamedRefWorktree(ctx, w, first, nil); err != nil {
				t.Fatalf("first checkout: %v", err)
			}
			build := filepath.Join(w.Dir, "build", "out.o")
			if err := os.MkdirAll(filepath.Dir(build), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(build, []byte("compiled\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(w.Dir, "stray.txt"), []byte("left by a run\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, leftover := range []string{filepath.Join(".sparkwing", "overlay.mod"), "go.work", "go.work.sum"} {
				if err := os.WriteFile(filepath.Join(w.Dir, leftover), []byte("left by a run\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			nested := filepath.Join(w.Dir, "vendored")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			runGitFixture(t, nested, "init", "--quiet")
			past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
			shared := filepath.Join(w.Dir, "shared.txt")
			if err := os.Chtimes(shared, past, past); err != nil {
				t.Fatal(err)
			}
			runGitFixture(t, w.Dir, "update-index", "--refresh")
			if err := os.WriteFile(filepath.Join(w.Dir, "edited.txt"), []byte("changed by a run\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := checkoutNamedRefWorktree(ctx, w, second, nil); err != nil {
				t.Fatalf("switch: %v", err)
			}

			if got := readFile(t, build); got != "compiled\n" {
				t.Errorf("ignored build output = %q, want it kept across the switch", got)
			}
			info, err := os.Stat(shared)
			if err != nil {
				t.Fatal(err)
			}
			if !info.ModTime().Equal(past) {
				t.Errorf("unchanged file's mtime = %s, want %s kept across the switch", info.ModTime(), past)
			}
			for _, leftover := range []string{"stray.txt", "vendored", filepath.Join(".sparkwing", "overlay.mod"), "go.work", "go.work.sum"} {
				if _, err := os.Stat(filepath.Join(w.Dir, leftover)); !os.IsNotExist(err) {
					t.Errorf("%s, which the commit does not hold, survived the switch: %v", leftover, err)
				}
			}
			if got := runGitFixture(t, w.Dir, "status", "--porcelain"); got != "" {
				t.Errorf("tracked files differ from the commit after the switch:\n%s", got)
			}
			if got := headCommit(t, w.Dir); got != second {
				t.Errorf("worktree is at %s, want %s", got, second)
			}
		})
	}
}

func TestNamedRefWorktreeRecoversFromWhatAnEarlierRunLeft(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	commitFile(t, repo, ".gitignore", "build/\n")
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")

	for name, tc := range map[string]struct {
		leave     func(t *testing.T, dir string)
		keepBuild bool
	}{
		"index lock from a cancelled checkout": {keepBuild: true, leave: func(t *testing.T, dir string) {
			lock := runGitFixture(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		"directory deleted behind git": {leave: func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}},
		"a worktree of another repository in its place": {leave: func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(t.TempDir(), "other")
			runGitFixture(t, repo, "clone", "--quiet", repo, other)
			runGitFixture(t, other, "worktree", "add", "--quiet", "--detach", dir)
		}},
		"a clone of the repository in its place": {leave: func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			runGitFixture(t, repo, "clone", "--quiet", repo, dir)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			p := paths.Paths{Root: t.TempDir()}
			w := namedWorktree(t, p, repo, "checks")
			if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
				t.Fatalf("first checkout: %v", err)
			}
			build := filepath.Join(w.Dir, "build", "out.o")
			if err := os.MkdirAll(filepath.Dir(build), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(build, []byte("compiled\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tc.leave(t, w.Dir)

			if err := checkoutNamedRefWorktree(t.Context(), w, second, nil); err != nil {
				t.Fatalf("checkout after %s: %v", name, err)
			}
			if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "second\n" {
				t.Errorf("worktree reads %q, want the second commit", got)
			}
			if got := runGitFixture(t, w.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); resolvedPath(got) != w.Repo {
				t.Errorf("worktree belongs to %s, want %s", got, w.Repo)
			}
			if _, err := os.Stat(build); (err == nil) != tc.keepBuild {
				t.Errorf("build output present = %v, want %v", err == nil, tc.keepBuild)
			}
		})
	}
}

func TestNamedRefWorktreeKeepsItsBuildWhenACancelInterruptsTheSwitch(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	commitFile(t, repo, ".gitignore", "build/\n")
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	build := filepath.Join(w.Dir, "build", "out.o")
	if err := os.MkdirAll(filepath.Dir(build), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(build, []byte("compiled\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checkoutNamedRefWorktree(ctx, w, second, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled switch returned %v, want context.Canceled", err)
	}
	if _, err := os.Stat(build); err != nil {
		t.Errorf("a cancelled switch cleared the build output: %v", err)
	}
}

func TestNamedRefWorktreeLeavesAnEnclosingCheckoutAlone(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	first := commitFile(t, repo, "version.txt", "first\n")
	commitFile(t, repo, "version.txt", "second\n")
	linked := filepath.Join(t.TempDir(), "linked")
	runGitFixture(t, repo, "worktree", "add", "--quiet", "--detach", linked)
	p := paths.Paths{Root: filepath.Join(linked, "home")}
	w := namedWorktree(t, p, repo, "checks")
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "version.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
		t.Fatalf("checkout into a directory with no .git: %v", err)
	}
	if got := readFile(t, filepath.Join(linked, "version.txt")); got != "edited\n" {
		t.Errorf("the enclosing checkout reads %q, want its edit left alone", got)
	}
	if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "first\n" {
		t.Errorf("worktree reads %q, want the first commit", got)
	}
}

func TestNamedRefWorktreeLeavesTheRepositoryItsGitFileWasAimedAt(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backPointer := filepath.Join(resolvedPath(filepath.Dir(w.Dir)), filepath.Base(w.Dir), ".git")
	if err := os.WriteFile(filepath.Join(repo, ".git", "gitdir"), []byte(backPointer+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch := runGitFixture(t, repo, "symbolic-ref", "--short", "HEAD")

	if err := checkoutNamedRefWorktree(t.Context(), w, second, nil); err != nil {
		t.Fatalf("checkout after the .git file was rewritten: %v", err)
	}
	if got := runGitFixture(t, repo, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Errorf("the repository's checkout moved to %q, want it left on %q", got, branch)
	}
	if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "second\n" {
		t.Errorf("worktree reads %q, want the second commit", got)
	}
}

func TestNamedRefWorktreeLeavesAnotherWorktreeOfTheRepositoryAlone(t *testing.T) {
	for name, aim := range map[string]func(t *testing.T, w NamedRefWorktree, mine string){
		".git file aimed at it": func(t *testing.T, w NamedRefWorktree, _ string) {
			if err := os.WriteFile(filepath.Join(w.Dir, ".git"), []byte("gitdir: "+filepath.Join(w.Repo, "worktrees", "mine")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"directory replaced by a symlink to it": func(t *testing.T, w NamedRefWorktree, mine string) {
			if err := os.RemoveAll(w.Dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(mine, w.Dir); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := gitRepoWithProject(t, true)
			first := commitFile(t, repo, "version.txt", "first\n")
			second := commitFile(t, repo, "version.txt", "second\n")
			mine := filepath.Join(t.TempDir(), "mine")
			runGitFixture(t, repo, "worktree", "add", "--quiet", "-b", "mywork", mine)
			if err := os.WriteFile(filepath.Join(mine, "wip.txt"), []byte("unsaved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p := paths.Paths{Root: t.TempDir()}
			w := namedWorktree(t, p, repo, "checks")
			if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
				t.Fatalf("first checkout: %v", err)
			}
			aim(t, w, mine)

			if err := checkoutNamedRefWorktree(t.Context(), w, second, nil); err != nil {
				t.Fatalf("checkout after %s: %v", name, err)
			}
			if got := runGitFixture(t, mine, "symbolic-ref", "--short", "HEAD"); got != "mywork" {
				t.Errorf("the other worktree is on %q, want it left on mywork", got)
			}
			if _, err := os.Stat(filepath.Join(mine, "wip.txt")); err != nil {
				t.Errorf("the other worktree's untracked work was removed: %v", err)
			}
			if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "second\n" {
				t.Errorf("worktree reads %q, want the second commit", got)
			}
		})
	}
}

func TestNamedRefWorktreeLeavesAnEnclosingDirectoryAimedAtItsRecords(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	first := commitFile(t, repo, "version.txt", "first\n")
	second := commitFile(t, repo, "version.txt", "second\n")
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, first, nil); err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	records := runGitFixture(t, w.Dir, "rev-parse", "--path-format=absolute", "--absolute-git-dir")
	if err := os.Remove(filepath.Join(w.Dir, ".git")); err != nil {
		t.Fatal(err)
	}
	enclosing := filepath.Dir(w.Dir)
	if err := os.WriteFile(filepath.Join(enclosing, ".git"), []byte("gitdir: "+records+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkoutNamedRefWorktree(t.Context(), w, second, nil); err != nil {
		t.Fatalf("checkout after its .git went missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(enclosing, "version.txt")); !os.IsNotExist(err) {
		t.Errorf("the checkout wrote into the enclosing directory: %v", err)
	}
	if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "second\n" {
		t.Errorf("worktree reads %q, want the second commit", got)
	}
}

func TestWaitNamedRefWorktreeFailsWhenItsLeaseCannotOpen(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := os.MkdirAll(p.NamedRefWorktreesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(w.Dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := waitNamedRefWorktree(ctx, w, nil)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; err == nil || errors.Is(err, context.Canceled) {
			t.Errorf("a lease that cannot open = %v, want its error rather than a wait", err)
		}
	})
}

func TestResolveNamedRefWorktreeFitsAnyRepositoryName(t *testing.T) {
	for _, base := range []string{strings.Repeat("r", 60), "my repo"} {
		repo := filepath.Join(t.TempDir(), base)
		if err := os.Rename(gitRepoWithProject(t, true), repo); err != nil {
			t.Fatal(err)
		}
		p := paths.Paths{Root: t.TempDir()}
		if w := namedWorktree(t, p, repo, "checks"); !withinNamedRefWorktrees(p, w.Dir) {
			t.Errorf("repository %q resolved %s, which no run can hold", base, w.Dir)
		}
	}
}

func TestNamedRefWorktreeIsSharedWithinARepositoryOnly(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitFixture(t, repo, "worktree", "add", "--quiet", "--detach", linked)
	other := filepath.Join(t.TempDir(), filepath.Base(repo))
	if err := os.Rename(gitRepoWithProject(t, true), other); err != nil {
		t.Fatal(err)
	}
	p := paths.Paths{Root: t.TempDir()}

	if a, b := namedWorktree(t, p, repo, "checks"), namedWorktree(t, p, linked, "checks"); a != b {
		t.Errorf("two checkouts of one repository resolved %s and %s, want one worktree", a.Dir, b.Dir)
	}
	if a, b := namedWorktree(t, p, repo, "checks"), namedWorktree(t, p, other, "checks"); a.Dir == b.Dir {
		t.Errorf("two repositories share %s", a.Dir)
	}
}

func TestResolveNamedRefWorktreeRefusesNamesThatAreNotOneDirectory(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	for _, name := range []string{"", ".hidden", "-flag", "../escape", "a/b", strings.Repeat("x", 65)} {
		if _, err := ResolveNamedRefWorktree(context.Background(), p, repo, name); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestWithinNamedRefWorktreesAcceptsOnlyAWorktreeDirectory(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	root := p.NamedRefWorktreesDir()
	cases := map[string]bool{
		filepath.Join(root, "repo-0123456789ab", "checks"): true,
		root:                                     false,
		filepath.Join(root, "repo-0123456789ab"): false,
		filepath.Join(root, "repo-0123456789ab", "checks", "deeper"): false,
		filepath.Join(root, "..", "elsewhere", "checks"):             false,
		filepath.Join(root, "repo-0123456789ab", ".."):               false,
		filepath.Join(root, "..", "checks"):                          false,
		filepath.Join(root, "repo-0123456789ab", ".hidden"):          false,
	}
	for dir, want := range cases {
		if got := withinNamedRefWorktrees(p, dir); got != want {
			t.Errorf("withinNamedRefWorktrees(%s) = %v, want %v", dir, got, want)
		}
	}

	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo-real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "repo-symlinked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "repo-real", "checks")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "repo-symlinked", "checks"), filepath.Join(root, "repo-real", "checks")} {
		if withinNamedRefWorktrees(p, dir) {
			t.Errorf("%s reaches outside the root through a symlink and was accepted", dir)
		}
	}
}

func TestWaitNamedRefWorktreeRefusesARunCancelledBeforeItWaits(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := waitNamedRefWorktree(cancelled, w, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("a run cancelled before it waited took a free worktree: %v", err)
	}
}

const namedWorktreeHolderDirEnv = "SPARKWING_TEST_NAMED_WORKTREE_DIR"

func TestNamedRefWorktreeHolderProcess(t *testing.T) {
	dir := os.Getenv(namedWorktreeHolderDirEnv)
	if dir == "" {
		t.Skip("run only as the child of TestNamedRefWorktreeFreesWhenItsHolderIsKilled")
	}
	if _, err := waitNamedRefWorktree(context.Background(), NamedRefWorktree{Dir: dir}, nil); err != nil {
		t.Fatalf("child hold: %v", err)
	}
	_, _ = os.Stdout.WriteString("held\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestNamedRefWorktreeFreesWhenItsHolderIsKilled(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")

	child := exec.Command(os.Args[0], "-test.run=^TestNamedRefWorktreeHolderProcess$")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	child.Env = append(os.Environ(), namedWorktreeHolderDirEnv+"="+w.Dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("child never held the worktree: %q, %v", line, err)
	}

	if !held(t, w) {
		t.Fatal("took a worktree another process holds")
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if held(t, w) {
		t.Fatal("the killed holder's worktree stayed held")
	}
}

func TestSweepNamedRefWorktreesRemovesOnlyThoseWhoseRepositoryIsGone(t *testing.T) {
	ctx := context.Background()
	p := paths.Paths{Root: t.TempDir()}
	kept := gitRepoWithProject(t, true)
	keptTree := namedWorktree(t, p, kept, "checks")
	if err := checkoutNamedRefWorktree(ctx, keptTree, headCommit(t, kept), nil); err != nil {
		t.Fatal(err)
	}

	gone := gitRepoWithProject(t, true)
	goneTree := namedWorktree(t, p, gone, "checks")
	heldTree := namedWorktree(t, p, gone, "held")
	for _, w := range []NamedRefWorktree{goneTree, heldTree} {
		if err := checkoutNamedRefWorktree(ctx, w, headCommit(t, gone), nil); err != nil {
			t.Fatal(err)
		}
	}
	creating := filepath.Join(filepath.Dir(goneTree.Dir), "creating")
	if err := os.MkdirAll(creating, 0o755); err != nil {
		t.Fatal(err)
	}
	hold, err := waitNamedRefWorktree(ctx, heldTree, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ReleaseRefWorktree(hold) }()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.NamedRefWorktreesDir(), "stray"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := sweepNamedRefWorktrees(p, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("sweep removed %d worktrees, want 1", removed)
	}
	if _, err := os.Stat(goneTree.Dir); !os.IsNotExist(err) {
		t.Errorf("worktree of a deleted repository survived: %v", err)
	}
	if _, err := os.Stat(heldTree.Dir); err != nil {
		t.Errorf("sweep removed a worktree a run holds: %v", err)
	}
	if _, err := os.Stat(keptTree.Dir); err != nil {
		t.Errorf("sweep removed a worktree whose repository remains: %v", err)
	}
	if _, err := os.Stat(creating); err != nil {
		t.Errorf("sweep removed a worktree whose .git is not written yet: %v", err)
	}
	if held(t, keptTree) || held(t, goneTree) {
		t.Error("sweep kept holding a worktree it was done with")
	}
	for dir, want := range map[string][]string{
		filepath.Dir(keptTree.Dir): {"checks", "checks.lease"},
		filepath.Dir(goneTree.Dir): {"checks.lease", "creating", "held", "held.lease"},
	} {
		var got []string
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got = append(got, e.Name())
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s holds %v after the sweep, want %v", dir, got, want)
		}
	}
}

func TestReclaimNamedRefWorktreeReleasesAWorktreeWhoseRepositoryReturned(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, headCommit(t, repo), nil); err != nil {
		t.Fatal(err)
	}
	if removed, err := reclaimNamedRefWorktree(w.Dir); removed || err != nil {
		t.Fatalf("reclaim of a live worktree = %v, %v; want it kept", removed, err)
	}
	if held(t, w) {
		t.Error("reclaim kept holding a worktree it left in place")
	}
}

func TestSweepNamedRefWorktreesOfAHomeWithNoneRemovesNothing(t *testing.T) {
	if removed, err := sweepNamedRefWorktrees(paths.Paths{Root: t.TempDir()}, nil); removed != 0 || err != nil {
		t.Errorf("sweep of a home with no named worktrees = %d, %v; want 0, nil", removed, err)
	}
}

func TestSweepNamedRefWorktreesReportsAWorktreeItCannotReclaim(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, headCommit(t, repo), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.Dir + refWorktreeLeaseSuffix); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.Dir+refWorktreeLeaseSuffix, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if removed, err := sweepNamedRefWorktrees(p, nil); removed != 0 || err == nil {
		t.Errorf("sweep of a worktree whose lease cannot open = %d, %v; want 0 and an error", removed, err)
	}
}

func TestSweepNamedRefWorktreesReportsWhatItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable directory")
	}
	p := paths.Paths{Root: t.TempDir()}
	locked := filepath.Join(p.NamedRefWorktreesDir(), "repo-0123456789ab")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if removed, err := sweepNamedRefWorktrees(p, nil); removed != 0 || err == nil {
		t.Errorf("sweep over an unreadable directory = %d, %v; want 0 and an error", removed, err)
	}
}

func TestConsumerHoldsTheNamedWorktreeForTheWholeDispatch(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	p := paths.Paths{Root: home}
	repo := gitRepoWithProject(t, true)
	commit := commitFile(t, repo, "version.txt", "submitted\n")
	commitFile(t, repo, "version.txt", "later\n")
	w := namedWorktree(t, p, repo, "checks")

	trig := namedTrigger("named-run", w, commit)
	trig.Pipeline, trig.CreatedAt = "build", time.Now()
	trig.TriggerEnv[SubmissionEnvironmentCapturedKey] = "1"
	if err := CaptureSubmissionEnvironment(home, trig.ID, os.Environ(), quietLogger()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(t.Context(), *trig); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextTrigger(t.Context(), time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v, %+v", err, claimed)
	}

	original := dispatchLocalTriggerFn
	t.Cleanup(func() { dispatchLocalTriggerFn = original })
	dispatched := false
	dispatchLocalTriggerFn = func(_ context.Context, _ *store.Trigger, _ string, _ *localCompileCache, _ *slog.Logger, _ []string, inherit ...*os.File) error {
		dispatched = true
		if len(inherit) != 1 {
			t.Errorf("the run's host inherits %d files, want the worktree's lease", len(inherit))
		}
		if !held(t, w) {
			t.Error("the run executed without holding its named worktree")
		}
		if got := readFile(t, filepath.Join(w.Dir, "version.txt")); got != "submitted\n" {
			t.Errorf("the run reads %q, want its submitted commit's file", got)
		}
		return nil
	}
	runClaimedTrigger(t.Context(), st, claimed, nil, quietLogger(), home, time.Minute)

	if !dispatched {
		t.Fatal("the run never dispatched")
	}
	if held(t, w) {
		t.Error("the named worktree stayed held after the run ended")
	}
}

func TestConsumerReleasesTheNamedWorktreeWhenTheCheckoutFails(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	runGitFixture(t, repo, "rm", "-r", "--quiet", ".sparkwing")
	commitFile(t, repo, "README.md", "no pipeline here\n")
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")

	if _, _, err := holdNamedRefWorktreeFor(t.Context(), p, namedTrigger("no-pipeline", w, headCommit(t, repo)), quietLogger()); err == nil {
		t.Fatal("a commit with no .sparkwing/ dispatched")
	}
	if held(t, w) {
		t.Error("a failed checkout left the named worktree held, so every later run would wait forever")
	}
}

func TestConsumerRefusesANamedRunItCannotTrust(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	w := namedWorktree(t, p, repo, "checks")
	symlinked := filepath.Join(filepath.Dir(w.Dir), "linked")
	if err := os.MkdirAll(filepath.Dir(symlinked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, symlinked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	commit := headCommit(t, repo)
	for name, trig := range map[string]*store.Trigger{
		"directory outside the worktree root": namedTrigger("forged", NamedRefWorktree{Dir: repo, Repo: w.Repo}, commit),
		"directory symlinked to a checkout":   namedTrigger("forged", NamedRefWorktree{Dir: symlinked, Repo: w.Repo}, commit),
		"no commit":                           namedTrigger("forged", w, ""),
		"a ref rather than a commit":          namedTrigger("forged", w, "HEAD"),
	} {
		if _, _, err := holdNamedRefWorktreeFor(t.Context(), p, trig, quietLogger()); err == nil {
			t.Errorf("a named run naming %s dispatched", name)
		}
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Errorf("a refused run created %s: %v", w.Dir, err)
	}
	if got := headCommit(t, repo); got != commit {
		t.Errorf("the submitting checkout moved to %s, want %s", got, commit)
	}
	if _, err := os.Stat(filepath.Join(repo, ".sparkwing")); err != nil {
		t.Errorf("a refused run removed the submitting checkout's files: %v", err)
	}
}

func TestConsumerSweepsNamedWorktreesWhoseRepositoryIsGoneWhenItStarts(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	repo := gitRepoWithProject(t, true)
	w := namedWorktree(t, paths.Paths{Root: home}, repo, "checks")
	if err := checkoutNamedRefWorktree(t.Context(), w, headCommit(t, repo), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	if err := ServeConsumer(t.Context(), ConsumerOptions{
		Home: home, Store: st, Logger: quietLogger(), IdleTimeout: time.Millisecond,
	}); err != nil {
		t.Fatalf("consumer: %v", err)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Errorf("a consumer start left the worktree of a deleted repository: %v", err)
	}
}

func TestConsumerSettlesANamedRunItRefuses(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	repo := gitRepoWithProject(t, true)
	trig := namedTrigger("forged", NamedRefWorktree{Dir: repo, Repo: namedWorktree(t, paths.Paths{Root: home}, repo, "checks").Repo}, headCommit(t, repo))
	trig.Pipeline, trig.CreatedAt = "build", time.Now()
	trig.TriggerEnv[SubmissionEnvironmentCapturedKey] = "1"
	if err := CaptureSubmissionEnvironment(home, trig.ID, os.Environ(), quietLogger()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(t.Context(), *trig); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextTrigger(t.Context(), time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v, %+v", err, claimed)
	}
	original := dispatchLocalTriggerFn
	t.Cleanup(func() { dispatchLocalTriggerFn = original })
	dispatchLocalTriggerFn = func(context.Context, *store.Trigger, string, *localCompileCache, *slog.Logger, []string, ...*os.File) error {
		t.Error("a refused named run dispatched")
		return nil
	}

	runClaimedTrigger(t.Context(), st, claimed, nil, quietLogger(), home, time.Minute)

	run, err := st.GetRun(t.Context(), trig.ID)
	if err != nil || run.Status != "failed" {
		t.Errorf("refused run = %+v, %v; want it recorded as failed", run, err)
	}
	if after, err := st.GetTrigger(t.Context(), trig.ID); err != nil || !after.IsFinished() {
		t.Errorf("refused run's trigger = %+v, %v; want it finished", after, err)
	}
}
