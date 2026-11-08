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

type landRemote struct {
	t     *testing.T
	bare  string
	dev   string
	other string
}

func newLandRemote(t *testing.T) *landRemote {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "gitconfig")
	writeFile(t, global, "[user]\n\tname = t\n\temail = t@example.com\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &landRemote{t: t, bare: filepath.Join(root, "origin.git"), dev: filepath.Join(root, "dev"), other: filepath.Join(root, "other")}
	r.git(root, "init", "--quiet", "--bare", "-b", "main", r.bare)
	r.git(root, "clone", "--quiet", r.bare, r.other)
	r.commit(r.other, map[string]string{"a/a.txt": "a1\na2\na3\n", "b/b.txt": "b1\n"}, "seed")
	r.git(r.other, "push", "--quiet", "origin", "HEAD:main")
	r.git(root, "clone", "--quiet", r.bare, r.dev)
	r.git(r.dev, "switch", "--quiet", "-c", "work")
	r.commit(r.dev, map[string]string{"a/a.txt": "a1\na2 branch\na3\n"}, "branch change")
	return r
}

func (r *landRemote) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *landRemote) commit(dir string, files map[string]string, msg string) string {
	r.t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		writeFile(r.t, path, body)
		r.git(dir, "add", name)
	}
	r.git(dir, "commit", "--quiet", "-m", msg)
	return r.git(dir, "rev-parse", "HEAD")
}

func (r *landRemote) moveMain(files map[string]string) string {
	r.t.Helper()
	r.git(r.other, "pull", "--quiet", "--ff-only", "origin", "main")
	commit := r.commit(r.other, files, "main moves")
	r.git(r.other, "push", "--quiet", "origin", "HEAD:main")
	return commit
}

func (r *landRemote) head() landHead {
	r.t.Helper()
	head, err := checkLandPreconditions(commandTestContext(r.t), gitIn(r.dev), r.dev)
	if err != nil {
		r.t.Fatal(err)
	}
	return head
}

func (r *landRemote) remoteMain() string {
	return r.git(r.bare, "rev-parse", "main")
}

type recordingRecheck struct {
	calls [][]string
	after func()
}

func (c *recordingRecheck) run(_ context.Context, files []string) error {
	c.calls = append(c.calls, slices.Clone(files))
	if c.after != nil {
		c.after()
	}
	return nil
}

func TestLandPushesTheGatedCommitWhenMainDidNotMove(t *testing.T) {
	r := newLandRemote(t)
	head := r.head()
	var recheck recordingRecheck
	landed, err := landOnto(commandTestContext(t), r.dev, head, recheck.run)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Commit != head.Commit || landed.Tree != head.Tree || landed.Merges != 0 || len(recheck.calls) != 0 {
		t.Fatalf("landed %+v with re-checks %v; want the gated commit %s pushed unmerged", landed, recheck.calls, short(head.Commit))
	}
	if got := r.remoteMain(); got != head.Commit {
		t.Fatalf("origin main = %s, want %s", short(got), short(head.Commit))
	}
}

func TestLandMergesMainThatMovedCleanlyAndRechecksOnlyTheMergeDelta(t *testing.T) {
	r := newLandRemote(t)
	head := r.head()
	moved := r.moveMain(map[string]string{"b/b.txt": "b1\nb2\n"})
	if again := r.head(); again != (landHead{Branch: head.Branch, Commit: head.Commit, Tree: head.Tree, Base: moved}) {
		t.Fatalf("preconditions on a branch behind a clean main = %+v", again)
	}
	var recheck recordingRecheck
	landed, err := landOnto(commandTestContext(t), r.dev, head, recheck.run)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Merges != 1 || landed.Base != moved {
		t.Fatalf("landed %+v, want one merge onto %s", landed, short(moved))
	}
	if got := r.remoteMain(); got != landed.Commit {
		t.Fatalf("origin main = %s, want the merge %s", short(got), short(landed.Commit))
	}
	if parents := r.git(r.dev, "rev-parse", landed.Commit+"^1", landed.Commit+"^2"); parents != head.Commit+"\n"+moved {
		t.Fatalf("merge parents = %q, want the gated commit then main", parents)
	}
	if tree := r.git(r.dev, "rev-parse", landed.Commit+"^{tree}"); tree != landed.Tree || tree == head.Tree {
		t.Fatalf("landed tree %s, merge commit's tree %s, gated tree %s: the record must name the pushed tree", short(landed.Tree), short(tree), short(head.Tree))
	}
	if got := r.git(r.dev, "rev-parse", "HEAD"); got != landed.Commit {
		t.Fatalf("the branch is at %s, want the merge it pushed %s", short(got), short(landed.Commit))
	}
	if len(recheck.calls) != 1 || !slices.Equal(recheck.calls[0], []string{"b/b.txt"}) {
		t.Fatalf("re-checks = %v, want one over main's change alone", recheck.calls)
	}
}

func TestLandMergeConflictFailsWithTheMergeToRunByHand(t *testing.T) {
	r := newLandRemote(t)
	head := r.head()
	moved := r.moveMain(map[string]string{"a/a.txt": "a1\na2 main\na3\n"})
	if _, err := checkLandPreconditions(commandTestContext(t), gitIn(r.dev), r.dev); err == nil || !strings.Contains(err.Error(), "merge origin/main") {
		t.Fatalf("preconditions err = %v, want the conflict refused with the merge remedy", err)
	}
	var recheck recordingRecheck
	_, err := landOnto(commandTestContext(t), r.dev, head, recheck.run)
	if err == nil || !strings.Contains(err.Error(), "git -C "+r.dev+" merge origin/main") || strings.Contains(err.Error(), "rebase") {
		t.Fatalf("err = %v, want the merge remedy and no rebase", err)
	}
	if got := r.remoteMain(); got != moved {
		t.Fatalf("origin main = %s, want it left at %s", short(got), short(moved))
	}
	if got := r.git(r.dev, "rev-parse", "HEAD"); got != head.Commit {
		t.Fatalf("the branch moved to %s after a failed merge", short(got))
	}
	if status := r.git(r.dev, "status", "--porcelain"); status != "" || len(recheck.calls) != 0 {
		t.Fatalf("status %q, re-checks %v: a conflicting merge must leave the checkout clean and check nothing", status, recheck.calls)
	}
}

func TestLandStopsAfterThreeMergesWhenMainKeepsMoving(t *testing.T) {
	r := newLandRemote(t)
	head := r.head()
	r.moveMain(map[string]string{"b/b.txt": "b1\nb2\n"})
	n := 0
	recheck := recordingRecheck{after: func() {
		n++
		r.moveMain(map[string]string{"c/c.txt": strings.Repeat("c\n", n)})
	}}
	_, err := landOnto(commandTestContext(t), r.dev, head, recheck.run)
	if err == nil || !strings.Contains(err.Error(), "moved again after 3 merges") {
		t.Fatalf("err = %v, want the bounded-merge refusal", err)
	}
	if len(recheck.calls) != landMaxMerges {
		t.Fatalf("re-checked %d times, want %d", len(recheck.calls), landMaxMerges)
	}
	if out, err := exec.Command("git", "-C", r.bare, "merge-base", "--is-ancestor", head.Commit, "main").CombinedOutput(); err == nil {
		t.Fatalf("the branch reached main although every push was refused: %s", out)
	}
}

func TestLandMergeDoesNotRunTheReviewAgain(t *testing.T) {
	jobs := landPlan(t)
	reviews := 0
	for id := range jobs {
		if strings.Contains(id, "review") {
			reviews++
		}
	}
	if reviews != 1 {
		t.Fatalf("land plans %d review jobs; a merge must not add a second review", reviews)
	}
	var steps []string
	for _, s := range jobs["land"].Steps {
		steps = append(steps, s.ID)
	}
	if !slices.Equal(steps, []string{"verify", "push", "record"}) {
		t.Fatalf("land steps = %v; the merge and its re-check belong to the push step", steps)
	}
}

func changelogWith(pairs ...string) string {
	body := "# Changelog\n\n## [Unreleased]\n"
	for i := 0; i+1 < len(pairs); i += 2 {
		body += "\n### " + pairs[i] + "\n\n- " + pairs[i+1] + "\n"
	}
	return body + "\n## [v0.1.0] - 2026-01-01\n\n### Added\n\n- seed\n"
}

// safety: the real check-changelog and sync-docs scripts, so the test fails when
// the --fix the land runs stops merging what a union merge repeats.
func (r *landRemote) seedChangelogTooling() {
	r.t.Helper()
	files := map[string]string{
		"CHANGELOG.md":          changelogWith(),
		"pkg/docs/changelog.md": changelogWith(),
		"docs/a.md":             "a\n",
		"pkg/docs/mirror/a.md":  "a\n",
		".gitattributes":        "CHANGELOG.md merge=union\npkg/docs/changelog.md merge=union\n",
	}
	for _, script := range []string{"check-changelog.sh", "sync-docs.sh"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "bin", script))
		if err != nil {
			r.t.Fatal(err)
		}
		files["bin/"+script] = string(body)
	}
	r.moveMain(files)
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.git(r.dev, "reset", "--quiet", "--hard", "origin/main")
}

func TestLandMergesTheChangelogHeadingsAUnionMergeRepeats(t *testing.T) {
	r := newLandRemote(t)
	r.seedChangelogTooling()
	r.commit(r.dev, map[string]string{"CHANGELOG.md": changelogWith("Added", "branch added", "Fixed", "branch entry"), "pkg/docs/changelog.md": changelogWith("Added", "branch added", "Fixed", "branch entry")}, "branch entry")
	head := r.head()
	moved := r.moveMain(map[string]string{"CHANGELOG.md": changelogWith("Fixed", "main entry"), "pkg/docs/changelog.md": changelogWith("Fixed", "main entry")})
	var recheck recordingRecheck
	landed, err := landOnto(commandTestContext(t), r.dev, head, recheck.run)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.remoteMain(); got != landed.Commit {
		t.Fatalf("origin main = %s, want the landed %s", short(got), short(landed.Commit))
	}
	if subject := r.git(r.dev, "log", "-1", "--format=%s", landed.Commit); subject != "chore(changelog): merge headings after main merge" {
		t.Fatalf("landed commit subject = %q, want the changelog follow-up on the merge", subject)
	}
	if parents := r.git(r.dev, "rev-parse", landed.Commit+"^^2"); parents != moved {
		t.Fatalf("the follow-up's parent merged %s, want main %s", short(parents), short(moved))
	}
	landedLog := r.git(r.dev, "show", landed.Commit+":CHANGELOG.md")
	if issues, err := changelogRepeatedHeadings(r.dev); err != nil || issues != "" {
		t.Fatalf("landed changelog still repeats a heading (%v):\n%s\n%s", err, issues, landedLog)
	}
	if !strings.Contains(landedLog, "branch entry") || !strings.Contains(landedLog, "main entry") {
		t.Fatalf("landed changelog lost an entry:\n%s", landedLog)
	}
	if mirror := r.git(r.dev, "show", landed.Commit+":pkg/docs/changelog.md"); mirror != landedLog {
		t.Fatalf("the mirror was not re-synced:\n%s", mirror)
	}
	if status := r.git(r.dev, "status", "--porcelain"); status != "" {
		t.Fatalf("the checkout is dirty after the land: %q", status)
	}
	if len(recheck.calls) != 1 || !slices.Contains(recheck.calls[0], "CHANGELOG.md") {
		t.Fatalf("re-checks = %v, want one covering the changelog", recheck.calls)
	}
}

func TestLandPreconditionsRefuseABranchChangelogThatRepeatsAHeading(t *testing.T) {
	r := newLandRemote(t)
	r.commit(r.dev, map[string]string{"CHANGELOG.md": changelogWith("Added", "one", "Added", "two")}, "repeat a heading")
	_, err := checkLandPreconditions(commandTestContext(t), gitIn(r.dev), r.dev)
	if err == nil || !strings.Contains(err.Error(), "check-changelog.sh --fix") {
		t.Fatalf("err = %v, want the repeated heading refused with the --fix remedy", err)
	}
}
