package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type WeeklyRelease struct{ sparkwing.Base }

var weeklyReleaseRuns = sparkwing.NewConcurrencyGroup("sparkwing-weekly-release", sparkwing.ConcurrencyLimit{Capacity: 1})

func (WeeklyRelease) ShortHelp() string {
	return "Release green main weekly, or now, when it has unreleased notes"
}

func (WeeklyRelease) Help() string {
	return "Pin origin/main (or --commit) in a detached worktree, skip an empty [Unreleased] section, require successful push CI at that exact SHA, then run release-cut from the selected source. Defaults to a v0 minor bump. Main can keep accepting lands. A per-version Bitwing sparkwing-release workflow records publication, docs, production verification and downstream pins. Dry runs preview without committing or pushing. A failed cut retains its worktree for inspection."
}

func (*WeeklyRelease) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseArgs, _ sparkwing.RunContext) error {
	plan.Concurrency(weeklyReleaseRuns)
	sparkwing.Job(plan, "release-main", &weeklyReleaseJob{RepoDir: sparkwing.WorkDir(), Args: in, Weekly: true}).
		Inline().Cores(lightJobCores).Timeout(90 * time.Minute)
	return nil
}

type weeklyReleaseJob struct {
	sparkwing.Base
	RepoDir string
	Args    ReleaseArgs
	Weekly  bool
}

func (j *weeklyReleaseJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	step := sparkwing.Step(w, "cut", func(ctx context.Context) error { return j.run(ctx, false) }).
		DryRun(func(ctx context.Context) error { return j.run(ctx, true) })
	if !j.Weekly {
		step.Risk("destructive", "prod")
	}
	return nil, nil
}

func (j *weeklyReleaseJob) run(ctx context.Context, dry bool) (err error) {
	tools, err := runGitIn(ctx, j.RepoDir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	tools = strings.TrimSpace(tools)
	selected := strings.TrimSpace(j.Args.Commit)
	if selected == "" {
		selected = "HEAD"
		if j.Weekly {
			selected = "refs/remotes/origin/main"
		}
	}
	// safety: HEAD is captured before fetching so the caller's moving branch cannot change the selection.
	if selected != "refs/remotes/origin/main" {
		selected, err = runGitIn(ctx, j.RepoDir, "rev-parse", "--verify", "--end-of-options", selected+"^{commit}")
		if err != nil {
			return err
		}
		selected = strings.TrimSpace(selected)
	}
	if err = fetchLandBase(ctx, gitIn(j.RepoDir), j.RepoDir); err != nil {
		return err
	}
	head, err := runGitIn(ctx, j.RepoDir, "rev-parse", "--verify", selected+"^{commit}")
	if err != nil {
		return err
	}
	head = strings.TrimSpace(head)
	if _, err = runGitIn(ctx, j.RepoDir, "merge-base", "--is-ancestor", head, landBaseRef); err != nil {
		return fmt.Errorf("release: selected commit %s must be on origin/main: %w", head, err)
	}
	dir, err := os.MkdirTemp("", "sparkwing-release-")
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			if _, cleanupErr := runGitIn(context.WithoutCancel(ctx), j.RepoDir, "worktree", "remove", filepath.Join(dir, "source")); cleanupErr != nil {
				sparkwing.Warn(ctx, "remove release worktree %s: %v", dir, cleanupErr)
				return
			}
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				sparkwing.Warn(ctx, "remove release checkout %s: %v", dir, cleanupErr)
			}
		} else {
			sparkwing.Info(ctx, "release checkout retained at %s", dir)
		}
	}()
	checkout := filepath.Join(dir, "source")
	if _, err = runGitIn(ctx, j.RepoDir, "worktree", "add", "--detach", checkout, head); err != nil {
		return err
	}
	body, err := readChangelog(checkout)
	if err != nil {
		return err
	}
	entries, err := unreleasedEntries(string(body))
	if err != nil {
		return err
	}
	if entries == 0 && j.Weekly {
		sparkwing.Info(ctx, "main has no unreleased notes; skipping release")
		return nil
	}
	ciErr := requireGreenMain(ctx, checkout, head)
	if ciErr != nil {
		if j.Weekly {
			return ciErr
		}
		sparkwing.Info(ctx, "exact-source CI unavailable; running local release checks: %v", ciErr)
	}
	version, err := (&resolveVersionJob{Explicit: j.Args.Version, Bump: j.Args.Bump, RepoDir: checkout}).run(ctx)
	if err != nil {
		return err
	}
	workflow := ""
	if !dry {
		result, err := sparkwing.Exec(ctx, "bitwing", "workflow", "start", "--name", "sparkwing-release",
			"--repo", "sparkwing", "--title", "Sparkwing "+version, "--idempotency-key", "sparkwing-release-"+version,
			"--context", "Weekly release authorized by schedule or manual invocation; version "+version, "-o", "json").Run()
		if err != nil {
			return fmt.Errorf("start release workflow: %w", err)
		}
		var run struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &run); err != nil {
			return err
		}
		if run.ID == "" {
			return errors.New("bitwing returned no release workflow run id")
		}
		workflow = run.ID
		if err := releaseWorkflowHandoff(ctx, workflow, "Cut starting from main@"+head+" for "+version+"."); err != nil {
			return err
		}
	}
	// perf: reuse exact-source CI evidence to avoid a broad desktop suite; retain the release-specific guards.
	args := []string{"run", "release-cut", "-C", checkout, "--sw-route", "local", "--sw-pipeline-ref", tools}
	if ciErr == nil {
		args = append(args, "--sw-start-at", "version-freshness")
	}
	if dry {
		args = append(args, "--sw-dry-run")
	} else {
		args = append(args, "--sw-allow-risk", "destructive,prod")
	}
	args = append(args, "--", "--version", version, "--commit", head)
	sparkwing.Info(ctx, "release source main@%s (%d unreleased entries)", head, entries)
	state := filepath.Join(dir, "state")
	defer func() {
		_, stopErr := sparkwing.Exec(context.WithoutCancel(ctx), "sparkwing", "daemon", "stop").
			Env("SPARKWING_HOME", state).Env("SPARKWING_SECRETS_KEY_FILE", filepath.Join(state, "secrets.key")).Run()
		if stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop private release controller: %w", stopErr))
		}
	}()
	_, err = sparkwing.Exec(ctx, "sparkwing", args...).Dir(checkout).
		Env("SPARKWING_HOME", state).Env("SPARKWING_SECRETS_KEY_FILE", filepath.Join(state, "secrets.key")).
		Env("GOWORK", "auto").Run()
	if err != nil {
		return err
	}
	if !dry {
		commit, err := runGitIn(ctx, checkout, "rev-parse", version+"^{commit}")
		if err != nil {
			return err
		}
		return releaseWorkflowHandoff(ctx, workflow, "Local release cut succeeded; immutable tag "+version+" at "+strings.TrimSpace(commit)+".")
	}
	return nil
}

func releaseWorkflowHandoff(ctx context.Context, run, cut string) error {
	note := cut + " Release owner: resume with bitwing workflow next --run " + run +
		". Confirm hosted publication and artifact identity, publish matching sparkwing-product docs, verify production through kikd-infra, and reconcile downstream pins before finishing this workflow."
	_, err := sparkwing.Exec(ctx, "bitwing", "workflow", "step", "--run", run, "--step", "cut", "--status", "blocked", "--note", note).Run()
	return err
}

type mainCIRun struct {
	HeadSHA    string `json:"headSha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func requireGreenMain(ctx context.Context, repo, sha string) error {
	result, err := sparkwing.Exec(ctx, "gh", "run", "list", "--workflow", "ci.yaml",
		"--branch", "main", "--event", "push", "--commit", sha, "--limit", "1",
		"--json", "headSha,status,conclusion").Dir(repo).Run()
	if err != nil {
		return fmt.Errorf("read main CI: %w", err)
	}
	return checkMainCI(result.Stdout, sha)
}

func checkMainCI(body, sha string) error {
	var runs []mainCIRun
	if err := json.Unmarshal([]byte(body), &runs); err != nil {
		return fmt.Errorf("read main CI receipt: %w", err)
	}
	if len(runs) != 1 || runs[0].HeadSHA != sha || runs[0].Status != "completed" || runs[0].Conclusion != "success" {
		return errors.New("main CI is absent, pending or failed at the selected SHA; release waits for green main")
	}
	return nil
}

func init() {
	sparkwing.Register[ReleaseArgs]("WeeklyRelease", func() sparkwing.Pipeline[ReleaseArgs] { return &WeeklyRelease{} })
}
