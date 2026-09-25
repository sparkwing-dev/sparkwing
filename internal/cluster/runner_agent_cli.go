package cluster

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/executorinfo"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func RunAgentCLI(args []string) error {
	return runAgentCLI(args)
}

func runAgentCLI(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "", "path to agent.yaml (default: ~/.config/sparkwing/agent.yaml)")
	var allowRepos multiFlag
	fs.Var(&allowRepos, "allow-repo",
		"repository this machine may build, as host/path with '*' matching within one path segment "+
			"(repeatable, e.g. --allow-repo 'github.com/acme/*'; replaces agent.yaml's allow_repos). "+
			"Without a gitcache in agent.yaml the agent then fetches each run's source directly, with the "+
			"credential the controller releases or else this machine's own git credentials, instead of "+
			"through the controller's gitcache proxy")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *configPath == "" {
		p, err := agentconfig.DefaultPath()
		if err != nil {
			return err
		}
		*configPath = p
	}

	raw, err := agentconfig.Load(*configPath)
	if err != nil {
		return err
	}
	if len(allowRepos) > 0 {
		raw.AllowRepos = allowRepos
	}
	cfg, err := agentconfig.Validate(*raw)
	if err != nil {
		return err
	}
	pool, err := agentPoolConfig(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger := slog.Default()
	logger.Info(
		"sparkwing agent starting",
		"config", *configPath,
		"labels", cfg.Labels,
		"max_concurrent", cfg.MaxConcurrent,
		"spawn_policy", cfg.SpawnPolicy,
		"allow_repo", pool.AllowRepos.String(),
		"direct_source", pool.GitcacheURL == "",
		"observed_platform", executorinfo.DetectObservedPlatform(),
	)
	return RunPoolLoop(ctx, pool, logger)
}

// agentPoolConfig is the claim loop a validated agent.yaml runs.
func agentPoolConfig(cfg agentconfig.Config) (PoolLoopConfig, error) {
	allow, err := sourceurl.ParseRepoAllowlist(cfg.AllowRepos)
	if err != nil {
		return PoolLoopConfig{}, fmt.Errorf("allow_repos: %w", err)
	}
	prefix := cfg.HolderPrefix
	if prefix == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			prefix = "agent:" + h
		} else {
			prefix = "agent"
		}
	}
	return PoolLoopConfig{
		ControllerURL: cfg.Controller, LogsURL: cfg.Logs, GitcacheURL: cfg.Gitcache, AllowRepos: allow,
		Token: cfg.Token, HolderPrefix: prefix,
		Labels: cfg.Labels, MaxConcurrent: cfg.MaxConcurrent, PollInterval: cfg.Poll,
		Lease: cfg.Lease, HeartbeatInterval: cfg.Heartbeat, SourceName: "agent",
		LocalAdmission: cfg.LocalAdmission != nil && *cfg.LocalAdmission, LocalReserve: cfg.LocalReserve,
		Contribution: cfg.Contribution,
	}, nil
}
