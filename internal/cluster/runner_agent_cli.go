package cluster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type AgentConfig struct {
	Controller    string        `yaml:"controller"`
	Logs          string        `yaml:"logs"`
	Gitcache      string        `yaml:"gitcache"`
	CacheToken    string        `yaml:"cache_token"`
	Profile       string        `yaml:"profile"`
	Token         string        `yaml:"token"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	ClaimPriority int           `yaml:"claim_priority"`
	Labels        []string      `yaml:"labels"`
	SpawnPolicy   string        `yaml:"spawn_policy"`
	HolderPrefix  string        `yaml:"holder_prefix"`
	Poll          time.Duration `yaml:"poll"`
	Lease         time.Duration `yaml:"lease"`
	Heartbeat     time.Duration `yaml:"heartbeat"`

	LocalAdmission bool `yaml:"local_admission"`

	LocalReserve string `yaml:"local_reserve"`
}

func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func ValidateAgentConfig(in AgentConfig) (AgentConfig, error) {
	out := in
	if out.Controller == "" {
		return out, errors.New("agent.yaml: controller is required")
	}
	if out.Gitcache == "" {
		out.Gitcache = strings.TrimRight(out.Controller, "/") + "/api/v1/gitcache"
	}
	if out.SpawnPolicy == "" {
		out.SpawnPolicy = "return-to-queue"
	}
	switch out.SpawnPolicy {
	case "return-to-queue":
	case "run-local", "auto":
		return out, fmt.Errorf("agent.yaml: spawn_policy %q is not implemented yet (only return-to-queue is supported in v0)", out.SpawnPolicy)
	default:
		return out, fmt.Errorf("agent.yaml: spawn_policy %q: expected return-to-queue | run-local | auto", out.SpawnPolicy)
	}
	if _, err := parseReserve(out.LocalReserve); err != nil {
		return out, fmt.Errorf("agent.yaml: local_reserve: %w", err)
	}
	if out.MaxConcurrent < 1 {
		out.MaxConcurrent = 1
	}
	if out.ClaimPriority < 0 || out.ClaimPriority > 100 {
		return out, fmt.Errorf("agent.yaml: claim_priority %d: expected 0 through 100", out.ClaimPriority)
	}
	if out.Poll <= 0 {
		out.Poll = 500 * time.Millisecond
	}
	if out.Lease <= 0 {
		out.Lease = store.DefaultLeaseDuration
	}
	clean := make([]string, 0, len(out.Labels))
	for _, l := range out.Labels {
		l = strings.TrimSpace(l)
		if l != "" {
			clean = append(clean, l)
		}
	}
	out.Labels = clean
	return out, nil
}

func DefaultAgentConfigPath() (string, error) {
	return fssecure.ConfigFile("agent.yaml")
}

func RunAgentCLI(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "", "path to agent.yaml (default: ~/.config/sparkwing/agent.yaml)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *configPath == "" {
		p, err := DefaultAgentConfigPath()
		if err != nil {
			return err
		}
		*configPath = p
	}

	raw, err := LoadAgentConfig(*configPath)
	if err != nil {
		return err
	}
	cfg, err := ValidateAgentConfig(*raw)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger := slog.Default()
	logger.Info(
		"sparkwing agent starting",
		"config", *configPath,
		"profile", cfg.Profile,
		"controller", cfg.Controller,
		"gitcache", cfg.Gitcache,
		"labels", cfg.Labels,
		"max_concurrent", cfg.MaxConcurrent,
		"claim_priority", cfg.ClaimPriority,
		"spawn_policy", cfg.SpawnPolicy,
		"auth", cfg.Token != "",
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
		ControllerURL:     cfg.Controller,
		LogsURL:           cfg.Logs,
		GitcacheURL:       cfg.Gitcache,
		CacheToken:        cfg.CacheToken,
		Token:             cfg.Token,
		HolderPrefix:      prefix,
		Labels:            cfg.Labels,
		ClaimPriority:     cfg.ClaimPriority,
		WorkerID:          prefix,
		ExecutorKind:      "direct",
		MaxConcurrent:     cfg.MaxConcurrent,
		PollInterval:      cfg.Poll,
		Lease:             cfg.Lease,
		HeartbeatInterval: cfg.Heartbeat,
		SourceName:        "agent",
		LocalAdmission:    cfg.LocalAdmission,
		LocalReserve:      cfg.LocalReserve,
	}, logger)
}
