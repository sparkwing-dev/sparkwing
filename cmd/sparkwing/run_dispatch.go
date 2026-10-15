package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type runFlags struct {
	invocation                 clisurface.Invocation
	parseErr                   error
	ref                        string
	publish, cacheFrom, target string
	pauseBefore, pauseAfter    []string
	pauseOnFailure             *bool

	noUpdate bool
	verbose  bool

	noBincache      bool
	hashAllFiles    bool
	allowUnadmitted bool

	workers int

	priority    string
	prioritySet bool

	startAt string
	stopAt  string

	only string

	noCache bool

	dryRun bool

	allow []string

	index string

	runHandleFile string

	detached       bool
	pipelineRef    string
	idempotencyKey string
	outputFormat   string
}

func validatePriorityFlag(v string) (string, error) {
	s := strings.TrimSpace(v)
	switch s {
	case wingwire.PriorityFront, wingwire.PriorityBack:
		return s, nil
	}
	if _, err := strconv.Atoi(s); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("--sw-priority %q: expected an integer, %q, or %q",
		v, wingwire.PriorityFront, wingwire.PriorityBack)
}

func parseRunFlags(args []string) (runFlags, []string) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"run", "pipeline"}, args...))
	if err != nil {
		if inv.Command.Path == "sparkwing run" && inv.Has("profile") {
			err = &clisurface.UsageError{Path: inv.Command.Path, Message: "--profile is not accepted by local run; use sparkwing pipeline trigger <pipeline> --profile NAME"}
		}
		return runFlags{parseErr: err}, nil
	}
	output := runOutput(inv, os.Getenv("SPARKWING_LOG_FORMAT"), color.IsInteractiveStdout())
	if output != "pretty" && output != "json" && output != "plain" {
		return runFlags{parseErr: errors.New("SPARKWING_LOG_FORMAT must be pretty, json or plain")}, nil
	}
	workers, _ := strconv.Atoi(inv.Value("sw-workers"))
	var pause *bool
	if inv.Has("sw-pause-on-failure") {
		value := inv.Bool("sw-pause-on-failure")
		pause = &value
	}
	return runFlags{
		invocation: inv,
		publish:    inv.Value("publish"), cacheFrom: inv.Value("sw-cache-from"), target: inv.Value("target"),
		pauseBefore: inv.Flags["sw-pause-before"], pauseAfter: inv.Flags["sw-pause-after"], pauseOnFailure: pause,
		ref: inv.Value("sw-ref"), pipelineRef: inv.Value("sw-pipeline-ref"),
		noUpdate: inv.Bool("sw-no-update"), verbose: inv.Bool("sw-verbose"),
		noBincache: inv.Bool("sw-no-bincache"), hashAllFiles: inv.Bool("sw-hash-all-files"),
		allowUnadmitted: inv.Bool("sw-allow-unadmitted"), workers: workers,
		priority: inv.Value("sw-priority"), prioritySet: inv.Has("sw-priority"),
		startAt: inv.Value("sw-start-at"), stopAt: inv.Value("sw-stop-at"),
		only: inv.Value("sw-only"), noCache: inv.Bool("sw-no-memo"),
		dryRun: inv.Bool("sw-dry-run"), allow: inv.Flags["sw-allow-risk"],
		index: inv.Value("sw-index"), runHandleFile: inv.Value("sw-run-handle-file"),
		detached: inv.Bool("sw-detached"), idempotencyKey: inv.Value("sw-idempotency-key"),
		outputFormat: output,
	}, inv.Args
}

func runOutput(inv clisurface.Invocation, environment string, terminal bool) string {
	if inv.Has("sw-output") {
		return inv.Value("sw-output")
	}
	if inv.Has("output") {
		return inv.Value("output")
	}
	if environment != "" {
		return environment
	}
	return inv.Output(terminal)
}

func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, pipelineDirectory string, cleanup func(), err error) {
	repoRoot := filepath.Dir(sparkwingDir)

	commit, err := orchestrator.ResolveRefCommit(context.Background(), repoRoot, ref, slog.Default())
	if err != nil {
		return "", "", nil, err
	}

	temporaryDir, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	// #nosec G702 -- the generated directory and resolved commit ID follow an explicit --.
	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", "--", temporaryDir, string(commit)).CombinedOutput()
	if err != nil {
		if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
			slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
		}
		return "", "", nil, fmt.Errorf("git worktree add %s: %w: %s",
			ref, err, strings.TrimSpace(string(out)))
	}

	pipelineDirectory = filepath.Join(temporaryDir, ".sparkwing")
	if fi, statErr := os.Stat(pipelineDirectory); statErr != nil || !fi.IsDir() {
		if cleanupErr := exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", "--", temporaryDir).Run(); cleanupErr != nil {
			slog.Warn("could not remove temporary Git worktree", "path", temporaryDir, "error", cleanupErr)
		}
		if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
			slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
		}
		return "", "", nil, fmt.Errorf("ref %s has no .sparkwing/ directory", ref)
	}

	// safety: more than one caller runs this, and the second `worktree remove`
	// of a gone path fails loudly.
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			// safety: Windows holds an open handle on a process's working
			// directory, and the exec path leaves it inside the worktree being
			// removed.
			if wd, wdErr := os.Getwd(); wdErr == nil {
				if rel, relErr := filepath.Rel(temporaryDir, wd); relErr == nil && !strings.HasPrefix(rel, "..") {
					if chdirErr := os.Chdir(repoRoot); chdirErr != nil {
						slog.Warn("could not leave the temporary worktree", "path", repoRoot, "error", chdirErr)
					}
				}
			}
			if cleanupErr := exec.Command("git", "-C", repoRoot,
				"worktree", "remove", "--force", "--", temporaryDir).Run(); cleanupErr != nil {
				slog.Warn("could not remove temporary Git worktree", "path", temporaryDir, "error", cleanupErr)
			}
			if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
				slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
			}
			// safety: git keeps the registration under the origin repository, where a
			// leftover one blocks adding the same path again.
			if err := exec.Command("git", "-C", repoRoot, "worktree", "prune").Run(); err != nil {
				slog.Default().Debug("ref worktree prune did not apply", "repo", repoRoot, "error", err)
			}
		})
	}
	return temporaryDir, pipelineDirectory, cleanup, nil
}

func parseGithubOwnerRepo(url string) string {
	if strings.HasPrefix(url, "git@github.com:") {
		rest := strings.TrimPrefix(url, "git@github.com:")
		rest = strings.TrimSuffix(rest, ".git")
		return rest
	}
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if strings.HasPrefix(url, prefix) {
			rest := strings.TrimPrefix(url, prefix)
			rest = strings.TrimSuffix(rest, ".git")
			return rest
		}
	}
	return ""
}
