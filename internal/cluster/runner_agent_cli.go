package cluster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// AgentConfig mirrors the on-disk layout of
// `~/.config/sparkwing/agent.yaml`. Fields optional unless noted.
type AgentConfig struct {
	Controller    string        `yaml:"controller"`
	Logs          string        `yaml:"logs"`
	Profile       string        `yaml:"profile"`
	Token         string        `yaml:"token"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	Labels        []string      `yaml:"labels"`
	SpawnPolicy   string        `yaml:"spawn_policy"`
	HolderPrefix  string        `yaml:"holder_prefix"`
	Poll          time.Duration `yaml:"poll"`
	Lease         time.Duration `yaml:"lease"`
	Heartbeat     time.Duration `yaml:"heartbeat"`
	// LocalAdmission routes controller-dispatched nodes through this box's
	// local admission daemon, so they share one FIFO queue with the
	// operator's own local runs. Set it on a box that both runs local
	// pipelines and serves this controller.
	LocalAdmission bool `yaml:"local_admission"`
	// LocalReserve is host capacity held back from what the agent
	// advertises to the controller (daemon budget grammar, e.g. "2,4gb" or
	// "10%"), so local work keeps room the controller will not fill.
	LocalReserve string `yaml:"local_reserve"`
}

// LoadAgentConfig reads an agent.yaml from path. Missing file is an
// error (we don't want the agent to silently claim without labels
// the user believed were set). Zero values for unspecified fields
// are normalized in ValidateAgentConfig.
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

// ValidateAgentConfig enforces required fields and applies defaults.
// Returns a normalized copy rather than mutating the input so callers
// can keep the on-disk shape for logging / diagnostics.
func ValidateAgentConfig(in AgentConfig) (AgentConfig, error) {
	out := in
	if out.Controller == "" {
		return out, errors.New("agent.yaml: controller is required")
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

// DefaultAgentConfigPath returns the canonical agent.yaml location.
// Honors XDG_CONFIG_HOME when set; otherwise falls back to
// ~/.config/sparkwing/agent.yaml.
func DefaultAgentConfigPath() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing", "agent.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "sparkwing", "agent.yaml"), nil
}

// RunAgentCLI implements `sparkwing agent` -- the laptop / off-cluster
// runner. Reads ~/.config/sparkwing/agent.yaml (or --config PATH),
// claims node work via the session-3 node-claim endpoints with the
// configured labels + bearer token, and executes via the shared
// RunPoolLoop code path.
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
		"labels", cfg.Labels,
		"max_concurrent", cfg.MaxConcurrent,
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
		Token:             cfg.Token,
		HolderPrefix:      prefix,
		Labels:            cfg.Labels,
		MaxConcurrent:     cfg.MaxConcurrent,
		PollInterval:      cfg.Poll,
		Lease:             cfg.Lease,
		HeartbeatInterval: cfg.Heartbeat,
		SourceName:        "agent",
		LocalAdmission:    cfg.LocalAdmission,
		LocalReserve:      cfg.LocalReserve,
	}, logger)
}
