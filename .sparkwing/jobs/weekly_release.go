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
	return "Clone origin/main into a private checkout, skip an empty [Unreleased] section, require the latest push CI run at that exact SHA to pass, then invoke the existing release pipeline. Defaults to a v0 minor bump. Pass --bump patch or --version for an earlier release. The tag workflow publishes the CLI, Go module and TypeScript package. A per-version Bitwing sparkwing-release workflow records the cut handoff for publication, docs, production verification and downstream pins. Dry runs preview the same release cut without committing or pushing. A failed cut retains its checkout for inspection."
}

func (*WeeklyRelease) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseArgs, _ sparkwing.RunContext) error {
	plan.Concurrency(weeklyReleaseRuns)
	sparkwing.Job(plan, "release-main", &weeklyReleaseJob{RepoDir: sparkwing.WorkDir(), Args: in}).
		Inline().Cores(lightJobCores).Timeout(90 * time.Minute)
	return nil
}

type weeklyReleaseJob struct {
	sparkwing.Base
	RepoDir string
	Args    ReleaseArgs
}

func (j *weeklyReleaseJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "cut", func(ctx context.Context) error { return j.run(ctx, false) }).
		DryRun(func(ctx context.Context) error { return j.run(ctx, true) })
	return nil, nil
}

func (j *weeklyReleaseJob) run(ctx context.Context, dry bool) (err error) {
	origin, err := runGitIn(ctx, j.RepoDir, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "sparkwing-release-")
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				sparkwing.Warn(ctx, "remove release checkout %s: %v", dir, cleanupErr)
			}
		} else {
			sparkwing.Info(ctx, "release checkout retained at %s", dir)
		}
	}()
	checkout := filepath.Join(dir, "source")
	if _, err = runGitIn(ctx, j.RepoDir, "clone", "--branch", "main", "--single-branch", "--", strings.TrimSpace(origin), checkout); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(checkout, "CHANGELOG.md"))
	if err != nil {
		return err
	}
	entries, err := unreleasedEntries(string(body))
	if err != nil {
		return err
	}
	if entries == 0 {
		sparkwing.Info(ctx, "main has no unreleased notes; skipping release")
		return nil
	}
	head, err := runGitIn(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head = strings.TrimSpace(head)
	if err = requireGreenMain(ctx, checkout, head); err != nil {
		return err
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
			return errors.New("Bitwing returned no release workflow run id")
		}
		workflow = run.ID
		if err := releaseWorkflowHandoff(ctx, workflow, "Cut starting from main@"+head+" for "+version+"."); err != nil {
			return err
		}
	}
	args := []string{"run", "release", "-C", checkout, "--sw-route", "local"}
	if dry {
		args = append(args, "--sw-dry-run")
	} else {
		args = append(args, "--sw-allow-risk", "destructive,prod")
	}
	args = append(args, "--", "--version", version)
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
		Env("GOWORK", "off").Run()
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
