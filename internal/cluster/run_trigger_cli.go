package cluster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func runTriggerCLI(args []string) error {
	fs := flag.NewFlagSet("run-trigger", flag.ContinueOnError)
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"), "controller base URL")
	logsURL := fs.String("logs", os.Getenv("SPARKWING_LOGS_URL"), "logs service URL")
	gitcacheURL := fs.String("gitcache", os.Getenv("SPARKWING_GITCACHE_URL"), "git cache URL; empty fetches source directly")
	workRoot := fs.String("work-root", "", "private directory for fetched source")
	var allowRepos multiFlag
	fs.Var(&allowRepos, "allow-repo", "repository this machine may build (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return errors.New("usage: sparkwing-runner run-trigger [flags] <run-id>")
	}
	if *controllerURL == "" {
		return errors.New("--controller or SPARKWING_CONTROLLER_URL is required")
	}
	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	if token == "" {
		return errors.New("SPARKWING_AGENT_TOKEN is required")
	}
	allow, err := sourceurl.ParseRepoAllowlist(allowRepos)
	if err != nil {
		return fmt.Errorf("--allow-repo: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return RunSpecificTrigger(ctx, fs.Arg(0), TriggerLoopOptions{
		ControllerURL: *controllerURL, LogsURL: *logsURL, GitcacheURL: *gitcacheURL,
		Token: token, RunnerKind: "inprocess", WorkRoot: *workRoot, AllowRepos: allow,
	})
}
