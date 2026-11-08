package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type OwnedSDKArgs struct {
	ReposRoot string `flag:"repos-root" desc:"Directory containing owned repositories. Default: ~/code"`
}

type OwnedSDK struct{ sparkwing.Base }

var ownedSDKRuns = sparkwing.NewConcurrencyGroup("sparkwing-owned-sdk-main", sparkwing.ConcurrencyLimit{Capacity: 1})

func (OwnedSDK) ShortHelp() string {
	return "Update owned Go pipelines to green Sparkwing main through Xwing landing"
}

func (OwnedSDK) Help() string {
	return "Inspect immediate children of --repos-root (default ~/code). Select Go pipeline repos owned by koreyGambill or sparkwing-dev whose committed Xwing policy lands main. Resolve green Sparkwing main to a Go module version, set the require and versioned replace to that exact source, tidy, commit in an Xwing environment, then run xwing env land with its configured verification. Skip unchanged repos and Sparkwing itself. Failed updates retain their environments and do not stop other repos. Dry runs list proposed pin edits without creating environments, committing or landing. TypeScript pipelines keep their published release pins."
}

func (*OwnedSDK) Plan(_ context.Context, plan *sparkwing.Plan, in OwnedSDKArgs, _ sparkwing.RunContext) error {
	plan.Concurrency(ownedSDKRuns)
	sparkwing.Job(plan, "update-owned-repos", &ownedSDKJob{RepoDir: sparkwing.WorkDir(), Args: in}).
		Inline().Cores(lightJobCores).Timeout(4 * time.Hour)
	return nil
}

type ownedSDKJob struct {
	sparkwing.Base
	RepoDir string
	Args    OwnedSDKArgs
}

func (j *ownedSDKJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "update", func(ctx context.Context) error { return j.run(ctx, false) }).
		DryRun(func(ctx context.Context) error { return j.run(ctx, true) })
	return nil, nil
}

func (j *ownedSDKJob) run(ctx context.Context, dry bool) error {
	root := j.Args.ReposRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, "code")
	}
	if !filepath.IsAbs(root) {
		return errors.New("--repos-root must be absolute")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	remote, err := runGitIn(ctx, j.RepoDir, "ls-remote", "origin", "refs/heads/main")
	if err != nil {
		return err
	}
	fields := strings.Fields(remote)
	if len(fields) != 2 {
		return errors.New("origin has no main")
	}
	sha := fields[0]
	if err := requireGreenMain(ctx, j.RepoDir, sha); err != nil {
		return err
	}
	result, err := sparkwing.Exec(ctx, "go", "list", "-m", "-json", sdkModulePath+"@"+sha).
		Dir(j.RepoDir).Env("GOWORK", "off").Run()
	if err != nil {
		return err
	}
	var target struct{ Version string }
	if err := json.Unmarshal([]byte(result.Stdout), &target); err != nil {
		return err
	}
	if !strings.HasPrefix(target.Version, "v0.") {
		return fmt.Errorf("main resolved outside v0.x: %q", target.Version)
	}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "sparkwing" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		repo := filepath.Join(root, entry.Name())
		if !ownedGoRepo(ctx, repo) {
			continue
		}
		if err := updateOwnedSDK(ctx, repo, entry.Name(), target.Version, sha, dry); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

var ownedOrigin = regexp.MustCompile(`(?i)^(?:git@github\.com:|https://github\.com/)(koreyGambill|sparkwing-dev)/[A-Za-z0-9_.-]+(?:\.git)?$`)

func ownedGoRepo(ctx context.Context, repo string) bool {
	if _, err := os.Stat(filepath.Join(repo, ".sparkwing", "go.mod")); err != nil {
		return false
	}
	body, err := os.ReadFile(filepath.Join(repo, ".xwing-env.yaml"))
	if err != nil {
		return false
	}
	var policy struct {
		Landing struct{ Branch, Verify, Delivery string }
	}
	if yaml.Unmarshal(body, &policy) != nil || policy.Landing.Branch != "main" || policy.Landing.Verify == "" || policy.Landing.Delivery == "pull-request" {
		return false
	}
	origin, err := runGitIn(ctx, repo, "remote", "get-url", "origin")
	return err == nil && ownedOrigin.MatchString(strings.TrimSpace(origin))
}

func ownedSDKCurrent(body []byte, version string) (bool, error) {
	f, err := modfile.Parse("go.mod", body, nil)
	if err != nil {
		return false, err
	}
	required, replaced := false, false
	for _, r := range f.Require {
		required = required || r.Mod.Path == sdkModulePath && r.Mod.Version == version
	}
	for _, r := range f.Replace {
		replaced = replaced || r.Old.Path == sdkModulePath && r.New.Path == sdkModulePath && r.New.Version == version
	}
	return required && replaced, nil
}

func updateOwnedSDK(ctx context.Context, repo, name, version, sha string, dry bool) error {
	body, err := runGitIn(ctx, repo, "show", "main:.sparkwing/go.mod")
	if err != nil {
		return err
	}
	current, err := ownedSDKCurrent([]byte(body), version)
	if err != nil || current {
		return err
	}
	if dry {
		sparkwing.Info(ctx, "dry-run: %s would require and replace Sparkwing with %s, then verify and land through Xwing", name, version)
		return nil
	}
	session := "sdk-main-" + name + "-" + sha[:12]
	if _, err := sparkwing.Exec(ctx, "xwing", "env", "create", "--name", session).Run(); err != nil {
		return err
	}
	result, err := sparkwing.Exec(ctx, "xwing", "env", "add", "--session", session, "--repo", repo,
		"--base", "origin/main", "--branch", "sdk-main-"+sha[:12], "-o", "json").Run()
	if err != nil {
		return err
	}
	var attached struct{ Path string }
	if err := json.Unmarshal([]byte(result.Stdout), &attached); err != nil {
		return err
	}
	if !filepath.IsAbs(attached.Path) {
		return errors.New("xwing returned no absolute worktree path")
	}
	sparkwing.Info(ctx, "%s update environment: %s", name, attached.Path)
	mod := filepath.Join(attached.Path, ".sparkwing")
	bodyBytes, err := os.ReadFile(filepath.Join(mod, "go.mod"))
	if err != nil {
		return err
	}
	current, err = ownedSDKCurrent(bodyBytes, version)
	if err != nil || current {
		return err
	}
	// safety: remove version-qualified replacements too, because they outrank the new all-version pin.
	f, err := modfile.Parse("go.mod", bodyBytes, nil)
	if err != nil {
		return err
	}
	edit := []string{"mod", "edit"}
	for _, r := range f.Replace {
		if r.Old.Path == sdkModulePath {
			edit = append(edit, "-dropreplace="+r.Old.String())
		}
	}
	edit = append(edit, "-require="+sdkModulePath+"@"+version, "-replace="+sdkModulePath+"="+sdkModulePath+"@"+version)
	if _, err := sparkwing.Exec(ctx, "go", edit...).Dir(mod).Env("GOWORK", "off").Run(); err != nil {
		return err
	}
	if _, err := sparkwing.Exec(ctx, "go", "mod", "tidy").Dir(mod).Env("GOWORK", "off").Run(); err != nil {
		return err
	}
	if _, err := runGitIn(ctx, attached.Path, "add", "--", ".sparkwing/go.mod", ".sparkwing/go.sum"); err != nil {
		return err
	}
	if _, err := runGitIn(ctx, attached.Path, "commit", "--no-verify", "-m", "chore(ci): follow Sparkwing main at "+sha[:12],
		"--", ".sparkwing/go.mod", ".sparkwing/go.sum"); err != nil {
		return err
	}
	_, err = sparkwing.Exec(ctx, "xwing", "env", "land", "--session", session, "--repo", name).Run()
	return err
}

func init() {
	sparkwing.Register[OwnedSDKArgs]("OwnedSDK", func() sparkwing.Pipeline[OwnedSDKArgs] { return &OwnedSDK{} })
}
