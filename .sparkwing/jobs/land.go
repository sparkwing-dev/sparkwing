package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Land checks a rebased branch, runs the gate and the review side by side, and
// fast-forwards origin/main to exactly the commit both passed.
type Land struct{ sparkwing.Base }

func (Land) ShortHelp() string {
	return "Land the current branch: gate and review in parallel, then fast-forward origin/main to HEAD"
}

func (Land) Help() string {
	return "Refuses a dirty checkout, a detached HEAD, main itself, and a branch that does not contain origin/main (after fetching it), " +
		"naming the command that fixes each. Then runs the gate (the same jobs as `sparkwing run gate`) and the review " +
		"(the same job as `sparkwing run review`, which reads .review.yaml and takes --brief and --skip) in parallel. " +
		"The review always runs, whatever the change's size or paths. When both pass and the review record's tree is HEAD's tree with result pass, " +
		"the land job re-checks that HEAD did not move and origin/main is still its ancestor, then runs `git push origin <HEAD>:refs/heads/main` " +
		"with no force. A rejected push means main moved: rebase and rerun. The push step carries the destructive and prod risks, " +
		"so a real landing needs --sw-allow-risk destructive,prod. The land job's output is the landing record: branch, commit, tree, base, " +
		"the run that gated it, and the review record's path and result."
}

func (Land) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Preview the landing without pushing", Command: "sparkwing run land --sw-dry-run"},
		{Comment: "Gate and review without pushing", Command: "sparkwing run land --sw-stop-at verify"},
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
	pre := sparkwing.Job(plan, "preconditions", &landPreconditionsJob{RepoDir: repoDir}).Inline()

	gate := gateJobs(plan, &Gate{}, "gate")
	needs := make([]sparkwing.Dep, 0, len(gate)+1)
	for _, n := range gate {
		n.Needs(pre)
		needs = append(needs, n)
	}
	review := reviewJobNode(plan, in, branch).Needs(pre)
	needs = append(needs, review)

	sparkwing.Job(plan, "land", &landJob{
		RepoDir: repoDir,
		RunID:   rc.RunID,
		Head:    sparkwing.RefTo[landHead](pre),
		Review:  sparkwing.RefTo[reviewRecord](review),
	}).Inline().Needs(needs...)
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
	if err := requireContainsBase(ctx, git, dir); err != nil {
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

func rebaseRemedy(dir string) string {
	return fmt.Sprintf("git -C %s fetch %s %s && git -C %s rebase %s, then rerun sparkwing run land", dir, landBaseRemote, landBaseBranch, dir, landBaseRef)
}

func requireContainsBase(ctx context.Context, git gitRunner, dir string) error {
	if _, err := git(ctx, "merge-base", "--is-ancestor", landBaseRef, "HEAD"); err != nil {
		return fmt.Errorf("land: HEAD does not contain %s, so main cannot fast-forward to it; rebase: %s", landBaseRef, rebaseRemedy(dir))
	}
	return nil
}

type landingRecord struct {
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	Tree         string `json:"tree"`
	Base         string `json:"base"`
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
}

func (j *landJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	verify := sparkwing.Step(w, "verify", func(ctx context.Context) error {
		return verifyLanding(ctx, gitIn(j.RepoDir), j.RepoDir, j.Head.Get(ctx), j.Review.Get(ctx))
	}).SafeWithoutDryRun()
	push := sparkwing.Step(w, "push", func(ctx context.Context) error {
		return pushLanding(ctx, gitIn(j.RepoDir), j.RepoDir, j.Head.Get(ctx).Commit)
	}).DryRun(func(ctx context.Context) error {
		sparkwing.Info(ctx, "dry run: would run git push %s", strings.Join(landPushArgs(j.Head.Get(ctx).Commit), " "))
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

func (j *landJob) record(ctx context.Context) (landingRecord, error) {
	head := j.Head.Get(ctx)
	review := j.Review.Get(ctx)
	common, err := gitIn(j.RepoDir)(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return landingRecord{}, fmt.Errorf("land: locate the git common dir: %w", err)
	}
	return landingRecord{
		Branch:       head.Branch,
		Commit:       head.Commit,
		Tree:         head.Tree,
		Base:         head.Base,
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
	return fmt.Sprintf("%s `%s` at `%s` on %s\n\n| | |\n| --- | --- |\n| tree | `%s` |\n| base | `%s` |\n| gate run | `%s` |\n| review | %s, `%s` |\n",
		verb, rec.Branch, short(rec.Commit), landBaseRef, rec.Tree, short(rec.Base), rec.GateRun, rec.ReviewResult, rec.ReviewRecord)
}

// safety: the gate and review judged the commit the preconditions accepted, so
// anything that moved HEAD, dirtied the checkout or let main move past it since
// means the evidence no longer describes what would be pushed.
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
	if err := fetchLandBase(ctx, git, dir); err != nil {
		return err
	}
	if _, err := git(ctx, "merge-base", "--is-ancestor", landBaseRef, head.Commit); err != nil {
		return fmt.Errorf("land: %s moved during the run and %s no longer contains it; rebase: %s", landBaseRef, short(head.Commit), rebaseRemedy(dir))
	}
	sparkwing.Info(ctx, "review %s on tree %s; %s is an ancestor of %s", review.Result, short(head.Tree), landBaseRef, short(head.Commit))
	return nil
}

// safety: no --force and an explicit commit, so the remote accepts the push
// only as a fast-forward to exactly the gated commit. --no-verify because the
// gate that just passed is a superset of the pre-push tier the hook would run.
func landPushArgs(commit string) []string {
	return []string{"push", "--no-verify", landBaseRemote, commit + ":refs/heads/" + landBaseBranch}
}

func pushLanding(ctx context.Context, git gitRunner, dir, commit string) error {
	out, err := git(ctx, landPushArgs(commit)...)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "rejected") {
			return fmt.Errorf("land: %s moved since the gate ran, so the push was refused: rebase: %s\n%w", landBaseRef, rebaseRemedy(dir), err)
		}
		return fmt.Errorf("land: push %s to %s: %w", short(commit), landBaseRef, err)
	}
	if out != "" {
		sparkwing.Info(ctx, "%s", out)
	}
	sparkwing.Info(ctx, "%s fast-forwarded to %s", landBaseRef, short(commit))
	return nil
}
