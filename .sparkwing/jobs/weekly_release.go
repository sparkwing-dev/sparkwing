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
	return "Clone origin/main into a private checkout, skip an empty [Unreleased] section, require the latest push CI run at that exact SHA to pass, then invoke the existing release pipeline. Defaults to a v0 minor bump. Pass --bump patch or --version for an earlier release. The tag workflow publishes the CLI, Go module and TypeScript package. Dry runs preview the same release cut without committing or pushing. A failed cut retains its checkout for inspection."
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
			_ = os.RemoveAll(dir)
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
	args := []string{"run", "release", "-C", checkout, "--sw-route", "local"}
	if dry {
		args = append(args, "--sw-dry-run")
	} else {
		args = append(args, "--sw-allow-risk", "destructive,prod")
	}
	args = append(args, "--")
	if j.Args.Version != "" {
		args = append(args, "--version", j.Args.Version)
	}
	if j.Args.Bump != "" {
		args = append(args, "--bump", j.Args.Bump)
	}
	sparkwing.Info(ctx, "release source main@%s (%d unreleased entries)", head, entries)
	_, err = sparkwing.Exec(ctx, "sparkwing", args...).Dir(checkout).
		Env("SPARKWING_HOME", filepath.Join(dir, "state")).Env("GOWORK", "off").Run()
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
