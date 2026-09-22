package cluster

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/executorinfo"
)

func RunAgentCLI(args []string) error {
	return runAgentCLI(args)
}

func runAgentCLI(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "", "path to agent.yaml (default: ~/.config/sparkwing/agent.yaml)")
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
	cfg, err := agentconfig.Validate(*raw)
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
		"observed_platform", executorinfo.DetectObservedPlatform(),
	)

	prefix := cfg.HolderPrefix
	if prefix == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			prefix = "agent:" + h
		} else {
			prefix = "agent"
		}
	}
	return RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL: cfg.Controller, LogsURL: cfg.Logs, GitcacheURL: cfg.Gitcache,
		Token: cfg.Token, HolderPrefix: prefix,
		Labels: cfg.Labels, MaxConcurrent: cfg.MaxConcurrent, PollInterval: cfg.Poll,
		Lease: cfg.Lease, HeartbeatInterval: cfg.Heartbeat, SourceName: "agent",
		LocalAdmission: cfg.LocalAdmission != nil && *cfg.LocalAdmission, LocalReserve: cfg.LocalReserve,
		Contribution: cfg.Contribution,
	}, logger)
}
