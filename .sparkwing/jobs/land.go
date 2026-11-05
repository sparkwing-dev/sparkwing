package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Land checks a branch, runs the gate and the review side by side, and pushes
// the commit both passed to main, merging origin/main into it first when main moved.
type Land struct{ sparkwing.Base }

func (Land) ShortHelp() string {
	return "Land the current branch: gate and review in parallel, then push HEAD to main, merging origin/main first when it moved"
}

func (Land) Help() string {
	return "Refuses a dirty checkout, a detached HEAD, main itself, and a branch whose merge with origin/main (after fetching it) conflicts, " +
		"naming the command that fixes each. Then runs the gate (the same jobs as `sparkwing run gate`) and the review " +
		"(the same job as `sparkwing run review`, which reads .review.yaml and takes --brief and --skip) in parallel. " +
		"The review always runs, whatever the change's size or paths. When both pass and the review record's tree is HEAD's tree with result pass, " +
		"the land job re-checks that HEAD did not move, fetches origin/main and runs `git push origin <HEAD>:refs/heads/main` " +
		"with no force, adding --no-verify when the pushed tree is the tree the gate and review judged, since the gate covers every check the pre-push hook makes; " +
		"a merged tree pushes through the hook. When origin/main is not an ancestor of HEAD, or the push is refused because main moved, the push step merges origin/main " +
		"into the branch (a merge commit under the default git identity, never a rebase), builds every module and runs the affected tests over the files " +
		"that differ between the gated tree and the merged tree, then pushes the merge; the review is not run again. It merges at most 3 times; a conflict fails " +
		"and names the merge to run by hand. The push step carries the destructive and prod risks, " +
		"so a real landing needs --sw-allow-risk destructive,prod. The land job's output is the landing record: branch, the commit and tree pushed, " +
		"the tree the gate and review judged, base, merges, the run that gated it, and the review record's path and result."
}

func (Land) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Preview the landing without pushing", Command: "sparkwing run land --sw-dry-run"},
		{Comment: "Gate and review without pushing (the risk check still covers the skipped push)", Command: "sparkwing run land --sw-stop-at verify --sw-allow-risk destructive,prod"},
		{Comment: "Land the branch on main", Command: "sparkwing run land --sw-allow-risk destructive,prod"},
	}
}

const (
	landBaseRemote = "origin"
	landBaseBranch = "main"
	landBaseRef    = landBaseRemote + "/" + landBaseBranch
)

func (p *Land) Plan(_ context.Context, plan *sparkwing.Plan, in ReviewArgs, rc sparkwing.RunContext) error {
	// safety: Plan reads WorkDir rather than running git, because planning does
	// no I/O. The inline jobs run git in this checkout, not a job snapshot, so
	// the commit they push is the one the developer is on.
	repoDir := sparkwing.WorkDir()
	if repoDir == "" {
		return errors.New("land: no working directory; run this pipeline from a checkout with a .sparkwing directory")
	}
	branch := ""
	if rc.Git != nil {
		branch = rc.Git.Branch
	}
	pre := sparkwing.Job(plan, "preconditions", &landPreconditionsJob{RepoDir: repoDir}).Inline().Cores(lightJobCores)

	gate := gateJobs(plan, &Gate{}, "gate", false)
	needs := make([]sparkwing.Dep, 0, len(gate)+1)
	for i, n := range gate {
		// safety: the last gate node records the tree and already waits for the others.
		if i < len(gate)-1 {
			n.Needs(pre)
		}
		needs = append(needs, n)
	}
	review := reviewJobNode(plan, in, branch).Needs(pre)
	needs = append(needs, review)

	sparkwing.Job(plan, "land", &landJob{
		RepoDir: repoDir,
		RunID:   rc.RunID,
		Head:    sparkwing.RefTo[landHead](pre),
		Review:  sparkwing.RefTo[reviewRecord](review),
	}).Inline().Cores(lightJobCores).Needs(needs...)
	return nil
}

func init() {
	sparkwing.Register[ReviewArgs]("Land", func() sparkwing.Pipeline[ReviewArgs] { return &Land{} })
}

type gitRunner func(ctx context.Context, args ...string) (string, error)

func gitIn(dir string) gitRunner {
	return func(ctx context.Context, args ...string) (string, error) {
		out, err := runGitIn(ctx, dir, args...)
		return strings.TrimSpace(out), err
	}
}

// safety: the commit the preconditions accepted; every later check compares against it.
type landHead struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
	Base   string `json:"base"`
}

type landPreconditionsJob struct {
	sparkwing.Base
	sparkwing.Produces[landHead]
	RepoDir string
}

func (j *landPreconditionsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "check", func(ctx context.Context) (landHead, error) {
		head, err := checkLandPreconditions(ctx, gitIn(j.RepoDir), j.RepoDir)
		if err != nil {
			return landHead{}, err
		}
		sparkwing.Info(ctx, "landing %s at %s (tree %s) onto %s %s", head.Branch, short(head.Commit), short(head.Tree), landBaseRef, short(head.Base))
		return head, nil
	}).SafeWithoutDryRun(), nil
}

func checkLandPreconditions(ctx context.Context, git gitRunner, dir string) (landHead, error) {
	branch, err := git(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		return landHead{}, fmt.Errorf("land: HEAD is detached; switch to the branch you want to land: git -C %s switch <branch>", dir)
	}
	if branch == landBaseBranch {
		return landHead{}, fmt.Errorf("land: the checkout is on %s; land from a work branch: git -C %s switch -c <branch>", landBaseBranch, dir)
	}
	if err := requireClean(ctx, git, dir); err != nil {
		return landHead{}, err
	}
	if err := fetchLandBase(ctx, git, dir); err != nil {
		return landHead{}, err
	}
	head, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return landHead{}, fmt.Errorf("land: resolve HEAD: %w", err)
	}
	tree, err := git(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return landHead{}, fmt.Errorf("land: resolve HEAD's tree: %w", err)
	}
	base, err := git(ctx, "rev-parse", landBaseRef)
	if err != nil {
		return landHead{}, fmt.Errorf("land: resolve %s: %w", landBaseRef, err)
	}
	if base == head {
		return landHead{}, fmt.Errorf("land: %s is already at HEAD %s, so there is nothing to land", landBaseRef, short(head))
	}
	if err := requireCleanMerge(ctx, git, dir); err != nil {
		return landHead{}, err
	}
	return landHead{Branch: branch, Commit: head, Tree: tree, Base: base}, nil
}

func requireClean(ctx context.Context, git gitRunner, dir string) error {
	status, err := git(ctx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("land: git status: %w", err)
	}
	if status != "" {
		return fmt.Errorf("land: the checkout has uncommitted changes, so the gated tree would not be the pushed one:\n%s\ncommit them (git -C %s add <paths> && git -C %s commit) or remove them, then rerun sparkwing run land", status, dir, dir)
	}
	return nil
}

func fetchLandBase(ctx context.Context, git gitRunner, dir string) error {
	if _, err := git(ctx, "fetch", "--quiet", landBaseRemote, landBaseBranch); err != nil {
		return fmt.Errorf("land: fetch %s %s to compare against current main: %w\nretry: git -C %s fetch %s %s", landBaseRemote, landBaseBranch, err, dir, landBaseRemote, landBaseBranch)
	}
	return nil
}

func mergeRemedy(dir string) string {
	return fmt.Sprintf("git -C %s fetch %s %s && git -C %s merge %s, then rerun sparkwing run land", dir, landBaseRemote, landBaseBranch, dir, landBaseRef)
}

// perf: a branch behind main that merges cleanly lands; the push step merges
// it after the gate. A conflict fails here, before the gate and review spend
// their time on a branch that cannot land unaided.
func requireCleanMerge(ctx context.Context, git gitRunner, dir string) error {
	if _, err := git(ctx, "merge-base", "--is-ancestor", landBaseRef, "HEAD"); err == nil {
		return nil
	}
	if _, err := git(ctx, "merge-tree", "--write-tree", "--no-messages", "HEAD", landBaseRef); err != nil {
		return fmt.Errorf("land: HEAD does not contain %s and merging it conflicts; merge it: %s", landBaseRef, mergeRemedy(dir))
	}
	sparkwing.Info(ctx, "%s moved past HEAD and merges cleanly; the push step merges it after the gate", landBaseRef)
	return nil
}

type landingRecord struct {
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	Tree         string `json:"tree"`
	GatedTree    string `json:"gated_tree"`
	Base         string `json:"base"`
	Merges       int    `json:"merges"`
	GateRun      string `json:"gate_run"`
	ReviewRecord string `json:"review_record"`
	ReviewResult string `json:"review_result"`
	Pushed       bool   `json:"pushed"`
}

type landJob struct {
	sparkwing.Base
	sparkwing.Produces[landingRecord]
	RepoDir string
	RunID   string
	Head    sparkwing.Ref[landHead]
	Review  sparkwing.Ref[reviewRecord]
	// safety: the push step sets it to what it pushed, so the record names the
	// pushed commit and tree rather than the gated ones.
	landed *landTarget
}

func (j *landJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	verify := sparkwing.Step(w, "verify", func(ctx context.Context) error {
		review, ok := j.review(ctx)
		if !ok {
			sparkwing.Info(ctx, "dry run: the review job has no dry-run body, so no review record is checked")
			review = reviewRecord{Tree: j.Head.Get(ctx).Tree, Result: resultPass}
		}
		return verifyLanding(ctx, gitIn(j.RepoDir), j.RepoDir, j.Head.Get(ctx), review)
	}).SafeWithoutDryRun()
	push := sparkwing.Step(w, "push", func(ctx context.Context) error {
		landed, err := landOnto(ctx, j.RepoDir, j.Head.Get(ctx), recheckMergeDelta)
		if err != nil {
			return err
		}
		j.landed = &landed
		return nil
	}).DryRun(func(ctx context.Context) error {
		sparkwing.Info(ctx, "dry run: would fetch %s, merge it into %s when it moved (at most %d times, re-checking the files each merge changed), then run git %s",
			landBaseRef, j.Head.Get(ctx).Branch, landMaxMerges, strings.Join(landPushArgs("<HEAD or the merge>", false), " "))
		return nil
	}).Risk("destructive", "prod").Needs(verify)
	return sparkwing.Step(w, "record", func(ctx context.Context) (landingRecord, error) {
		rec, err := j.record(ctx)
		if err != nil {
			return landingRecord{}, err
		}
		sparkwing.Summary(ctx, landingSummary(rec))
		return rec, nil
	}).SafeWithoutDryRun().Needs(push), nil
}

// safety: only a dry run, whose review job does not run, may land without a
// review record; a real run still fails on the absent output.
// bug: the host reports a dry-run job's missing output as a read failure
// rather than an absence, so TryGet panics; a dry run must not resolve it.
func (j *landJob) review(ctx context.Context) (reviewRecord, bool) {
	if sparkwing.IsDryRun(ctx) {
		return reviewRecord{}, false
	}
	return j.Review.Get(ctx), true
}

func (j *landJob) record(ctx context.Context) (landingRecord, error) {
	head := j.Head.Get(ctx)
	review, ok := j.review(ctx)
	if !ok {
		review = reviewRecord{Branch: head.Branch, Result: "not run (dry run)"}
	}
	common, err := gitIn(j.RepoDir)(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return landingRecord{}, fmt.Errorf("land: locate the git common dir: %w", err)
	}
	pushed := landTarget{Commit: head.Commit, Tree: head.Tree, Base: head.Base}
	if j.landed != nil {
		pushed = *j.landed
	}
	return landingRecord{
		Branch:       head.Branch,
		Commit:       pushed.Commit,
		Tree:         pushed.Tree,
		GatedTree:    head.Tree,
		Base:         pushed.Base,
		Merges:       pushed.Merges,
		GateRun:      j.RunID,
		ReviewRecord: reviewRecordPath(filepath.Join(common, "sparkwing-review"), review.Branch),
		ReviewResult: review.Result,
		Pushed:       !sparkwing.IsDryRun(ctx),
	}, nil
}

func landingSummary(rec landingRecord) string {
	verb := "Landed"
	if !rec.Pushed {
		verb = "Dry run: would land"
	}
	return fmt.Sprintf("%s `%s` at `%s` on %s\n\n| | |\n| --- | --- |\n| tree | `%s` |\n| gated tree | `%s` |\n| merges of %s | %d |\n| base | `%s` |\n| gate run | `%s` |\n| review | %s, `%s` |\n",
		verb, rec.Branch, short(rec.Commit), landBaseRef, rec.Tree, rec.GatedTree, landBaseRef, rec.Merges, short(rec.Base), rec.GateRun, rec.ReviewResult, rec.ReviewRecord)
}

// safety: the gate and review judged the commit the preconditions accepted, so
// anything that moved HEAD or dirtied the checkout since means the evidence no
// longer describes what would be pushed. Main moving is the push step's to handle.
func verifyLanding(ctx context.Context, git gitRunner, dir string, head landHead, review reviewRecord) error {
	if review.Result != resultPass {
		return fmt.Errorf("land: the review record's result is %q with %d blocking finding(s) open; fix them and rerun sparkwing run land", review.Result, review.BlockingOpen)
	}
	if review.Tree != head.Tree {
		return fmt.Errorf("land: the review judged tree %s but HEAD's tree is %s; rerun sparkwing run land", short(review.Tree), short(head.Tree))
	}
	now, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("land: resolve HEAD: %w", err)
	}
	if now != head.Commit {
		return fmt.Errorf("land: HEAD moved from %s to %s during the run, so the gate and review judged another commit; rerun sparkwing run land", short(head.Commit), short(now))
	}
	if err := requireClean(ctx, git, dir); err != nil {
		return err
	}
	sparkwing.Info(ctx, "review %s on tree %s, HEAD still %s", review.Result, short(head.Tree), short(head.Commit))
	return nil
}

// safety: bounded, so a main that keeps moving fails the land instead of
// merging and re-checking without end.
const landMaxMerges = 3

// safety: Tree is the tree pushed. Only a merge changes it from the gated
// tree, and the merge's own delta is re-checked before the push.
type landTarget struct {
	Commit string
	Tree   string
	Base   string
	Merges int
}

type mergeRecheck func(ctx context.Context, files []string) error

// safety: a refusal because main moved sends land back to merge; any other
// push failure ends the land.
type errMainMoved struct{ error }

// perf: main's own commits passed their land, and the gate and review judged
// this branch, so a clean merge re-checks only the files the merge changed
// The review covers the branch's diff and is not run again.
func landOnto(ctx context.Context, dir string, head landHead, recheck mergeRecheck) (landTarget, error) {
	git := gitIn(dir)
	target := landTarget{Commit: head.Commit, Tree: head.Tree, Base: head.Base}
	var refused error
	for {
		if err := fetchLandBase(ctx, git, dir); err != nil {
			return target, err
		}
		base, err := git(ctx, "rev-parse", landBaseRef)
		if err != nil {
			return target, fmt.Errorf("land: resolve %s: %w", landBaseRef, err)
		}
		if refused != nil && base == target.Base {
			return target, refused
		}
		if _, err := git(ctx, "merge-base", "--is-ancestor", base, target.Commit); err != nil {
			if target.Merges == landMaxMerges {
				return target, fmt.Errorf("land: %s moved again after %d merges; rerun sparkwing run land when main is quieter", landBaseRef, landMaxMerges)
			}
			target, err = mergeLandBase(ctx, git, dir, head.Branch, target, base)
			if err != nil {
				return target, err
			}
			files, err := changedBetween(ctx, dir, head.Tree, target.Tree)
			if err != nil {
				return target, err
			}
			sparkwing.Info(ctx, "merged %s %s into %s as %s; re-checking the %d file(s) the merge changed", landBaseRef, short(base), head.Branch, short(target.Commit), len(files))
			if err := recheck(ctx, files); err != nil {
				return target, fmt.Errorf("land: %s merged cleanly as %s, which the branch now holds, but re-checking the files it changed failed; fix it on the branch and rerun sparkwing run land: %w", landBaseRef, short(target.Commit), err)
			}
		}
		target.Base = base
		refused = pushLanding(ctx, git, dir, target.Commit, head.Tree)
		if refused == nil {
			return target, nil
		}
		if !errors.As(refused, new(errMainMoved)) {
			return target, refused
		}
	}
}

// safety: the merge goes through git merge in the checkout, so it carries the
// developer's identity and signing, and the branch holds what main receives.
// A merge that stops is aborted, leaving the checkout as the gate judged it.
func mergeLandBase(ctx context.Context, git gitRunner, dir, branch string, onto landTarget, base string) (landTarget, error) {
	now, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return onto, fmt.Errorf("land: resolve HEAD: %w", err)
	}
	if now != onto.Commit {
		return onto, fmt.Errorf("land: HEAD moved from %s to %s during the run; rerun sparkwing run land", short(onto.Commit), short(now))
	}
	if err := requireClean(ctx, git, dir); err != nil {
		return onto, err
	}
	msg := fmt.Sprintf("Merge remote-tracking branch '%s' into %s", landBaseRef, branch)
	if _, err := git(ctx, "merge", "--no-ff", "--no-edit", "-m", msg, base); err != nil {
		if _, merging := git(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD"); merging == nil {
			if _, abortErr := git(ctx, "merge", "--abort"); abortErr != nil {
				err = errors.Join(err, abortErr)
			}
		}
		return onto, fmt.Errorf("land: merging %s %s into %s conflicts, so it cannot land unaided; merge it: %s\n%w", landBaseRef, short(base), branch, mergeRemedy(dir), err)
	}
	commit, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return onto, fmt.Errorf("land: resolve the merge commit: %w", err)
	}
	tree, err := git(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return onto, fmt.Errorf("land: resolve the merge commit's tree: %w", err)
	}
	return landTarget{Commit: commit, Tree: tree, Base: base, Merges: onto.Merges + 1}, nil
}

// safety: -z and no trimming, so a name holding a newline or leading space
// still reaches the affected-test selection intact.
func changedBetween(ctx context.Context, dir, from, to string) ([]string, error) {
	out, err := runGitIn(ctx, dir, "diff", "-z", "--name-only", "--no-renames", "--diff-filter=ACMRD", from, to)
	if err != nil {
		return nil, fmt.Errorf("land: list the files the merge changed: %w", err)
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// safety: build catches an importer the merge broke, which the affected
// selection leaves to the build step; the tests cover the packages it changed.
func recheckMergeDelta(ctx context.Context, files []string) error {
	if err := runBuild(ctx); err != nil {
		return err
	}
	if err := ensureFrontendDeps(ctx); err != nil {
		return err
	}
	return runAffectedTestFiles(ctx, files, fmt.Sprintf("%d file(s) the merge of %s changed", len(files), landBaseRef))
}

// safety: no --force and an explicit commit, so the remote accepts the push
// only as a fast-forward to exactly the gated commit.
// perf: the gate runs every check the pre-push hook makes, the qatrust vet
// included, so re-running the hook on the tree the gate just passed only queued
// behind admission (8.4 minutes of a 632 s push in land #587).
func landPushArgs(commit string, gated bool) []string {
	if gated {
		return []string{"push", "--no-verify", landBaseRemote, commit + ":refs/heads/" + landBaseBranch}
	}
	return []string{"push", landBaseRemote, commit + ":refs/heads/" + landBaseBranch}
}

// safety: the hook is skipped only when the pushed commit's tree is the one the
// gate and review judged; anything else is judged by the hook as a human push is.
func pushLanding(ctx context.Context, git gitRunner, dir, commit, gatedTree string) error {
	tree, err := git(ctx, "rev-parse", commit+"^{tree}")
	gated := err == nil && tree == gatedTree
	if !gated {
		sparkwing.Info(ctx, "the pushed tree is not the gated tree %s, so the pre-push hook judges it", short(gatedTree))
	}
	out, err := git(ctx, landPushArgs(commit, gated)...)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "rejected") {
			return errMainMoved{fmt.Errorf("land: %s moved since the gate ran, so the push was refused; merge it: %s\n%w", landBaseRef, mergeRemedy(dir), err)}
		}
		return fmt.Errorf("land: push %s to %s: %w", short(commit), landBaseRef, err)
	}
	if out != "" {
		sparkwing.Info(ctx, "%s", out)
	}
	sparkwing.Info(ctx, "%s fast-forwarded to %s", landBaseRef, short(commit))
	return nil
}
