package cluster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/executorinfo"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type AgentConfig struct {
	Name         string                   `yaml:"name"`
	Contribution string                   `yaml:"contribution"`
	Coordinators []AgentCoordinatorConfig `yaml:"coordinators"`

	Controller    string        `yaml:"controller"`
	Logs          string        `yaml:"logs"`
	Gitcache      string        `yaml:"gitcache"`
	CacheToken    string        `yaml:"cache_token"`
	Profile       string        `yaml:"profile"`
	Token         string        `yaml:"token"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	Labels        []string      `yaml:"labels"`
	SpawnPolicy   string        `yaml:"spawn_policy"`
	HolderPrefix  string        `yaml:"holder_prefix"`
	Poll          time.Duration `yaml:"poll"`
	Lease         time.Duration `yaml:"lease"`
	Heartbeat     time.Duration `yaml:"heartbeat"`

	LocalAdmission *bool `yaml:"local_admission"`

	LocalReserve string `yaml:"local_reserve"`

	registered bool
}

// AgentCoordinatorConfig is one explicitly enrolled controller membership.
// Each membership carries a distinct credential and may narrow the global
// slot and contribution ceilings.
type AgentCoordinatorConfig struct {
	Name          string `yaml:"name"`
	Controller    string `yaml:"controller"`
	Logs          string `yaml:"logs"`
	Gitcache      string `yaml:"gitcache"`
	CacheToken    string `yaml:"cache_token"`
	Profile       string `yaml:"profile"`
	Token         string `yaml:"token"`
	MaxConcurrent int    `yaml:"max_concurrent"`
	Contribution  string `yaml:"contribution"`
}

func LoadAgentConfig(path string) (*AgentConfig, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var cfg AgentConfig
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func ValidateAgentConfig(in AgentConfig) (AgentConfig, error) {
	out := in
	if out.Controller == "" && len(out.Coordinators) == 0 {
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
	if _, err := parseReserve(out.Contribution); err != nil {
		return out, fmt.Errorf("agent.yaml: contribution: %w", err)
	}
	out.Name = strings.TrimSpace(out.Name)
	out.registered = out.Name != "" || len(out.Coordinators) > 0
	if out.registered && strings.Contains(out.Name, ":") {
		return out, errors.New("agent.yaml: name is required and cannot contain ':'")
	}
	if out.registered {
		if out.LocalAdmission != nil && !*out.LocalAdmission {
			return out, errors.New("agent.yaml: local_admission cannot be false for enrolled helper memberships")
		}
		required := true
		out.LocalAdmission = &required
	} else if out.LocalAdmission == nil {
		disabled := false
		out.LocalAdmission = &disabled
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
	seen := map[string]bool{}
	for _, l := range out.Labels {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] {
			seen[l] = true
			clean = append(clean, l)
		}
	}
	out.Labels = clean
	membershipTokens := map[string]bool{}
	membershipControllers := map[string]bool{}
	for i := range out.Coordinators {
		member := &out.Coordinators[i]
		if member.Controller == "" {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].controller is required", i)
		}
		if membershipControllers[member.Controller] {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].controller is enrolled more than once", i)
		}
		membershipControllers[member.Controller] = true
		if member.Name == "" {
			member.Name = out.Name
		}
		member.Name = strings.TrimSpace(member.Name)
		if member.Name == "" || strings.Contains(member.Name, ":") {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].name is required and cannot contain ':'", i)
		}
		if member.Token == "" {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].token is required", i)
		}
		if membershipTokens[member.Token] {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].token must be distinct", i)
		}
		membershipTokens[member.Token] = true
		if member.Gitcache == "" {
			member.Gitcache = strings.TrimRight(member.Controller, "/") + "/api/v1/gitcache"
		}
		if member.MaxConcurrent <= 0 || member.MaxConcurrent > out.MaxConcurrent {
			member.MaxConcurrent = out.MaxConcurrent
		}
		if member.Contribution == "" {
			member.Contribution = out.Contribution
		}
		if _, err := parseReserve(member.Contribution); err != nil {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].contribution: %w", i, err)
		}
	}
	if out.registered && len(out.Coordinators) == 0 && out.Token == "" {
		return out, errors.New("agent.yaml: token is required for an enrolled helper membership")
	}
	return out, nil
}

func agentCoordinators(cfg AgentConfig) []AgentCoordinatorConfig {
	if len(cfg.Coordinators) > 0 {
		return cfg.Coordinators
	}
	return []AgentCoordinatorConfig{{
		Name:       cfg.Name,
		Controller: cfg.Controller, Logs: cfg.Logs, Gitcache: cfg.Gitcache,
		CacheToken: cfg.CacheToken, Profile: cfg.Profile, Token: cfg.Token,
		MaxConcurrent: cfg.MaxConcurrent, Contribution: cfg.Contribution,
	}}
}

func runAgentMembership(ctx context.Context, cfg AgentConfig, member AgentCoordinatorConfig, ledger ExecutorCapacityLedger, logger *slog.Logger) error {
	limits, err := executorCapacityLimitsFor(cfg, member)
	if err != nil {
		return err
	}
	provider := newHeadroomProvider("", "", limits.localReserve, limits.globalContribution, limits.membershipContribution)
	ctrl := client.NewWithToken(member.Controller, &http.Client{Timeout: 30 * time.Second}, member.Token)
	exec := func(execCtx context.Context, n *store.Node, holderID string, admission *orchestrator.LocalAdmission) {
		executePooledNode(execCtx, ctrl, member.Controller, member.Logs, member.Gitcache, member.Token, member.CacheToken,
			n, holderID, cfg.Lease, cfg.Heartbeat, "agent", logger, admission, provider)
	}
	return runAgentMembershipLoop(ctx, cfg, member, provider, ctrl, ledger, exec, logger)
}

func executorCapacityLimitsFor(cfg AgentConfig, member AgentCoordinatorConfig) (executorCapacityLimits, error) {
	localReserve, err := parseReserve(cfg.LocalReserve)
	if err != nil {
		return executorCapacityLimits{}, err
	}
	globalContribution, err := parseReserve(cfg.Contribution)
	if err != nil {
		return executorCapacityLimits{}, err
	}
	membershipContribution, err := parseReserve(member.Contribution)
	if err != nil {
		return executorCapacityLimits{}, err
	}
	return executorCapacityLimits{
		localReserve: localReserve, globalContribution: globalContribution, membershipContribution: membershipContribution,
	}, nil
}

type executorMembershipClient interface {
	HeartbeatExecutor(context.Context, string, client.Headroom) error
	PrepareExecutorClaim(context.Context, string) (*store.ExecutorClaimPreparation, error)
	OfferExecutorClaim(context.Context, client.ExecutorClaim, string, string) (client.ExecutorClaimOfferResult, error)
}

type executorNodeFn func(context.Context, *store.Node, string, *orchestrator.LocalAdmission)

const (
	executorClaimRequestTimeout  = 2 * time.Second
	executorOfferTransportBudget = 500 * time.Millisecond
)

func runAgentMembershipLoop(ctx context.Context, cfg AgentConfig, member AgentCoordinatorConfig, provider headroomProvider, ctrl executorMembershipClient, ledger ExecutorCapacityLedger, exec executorNodeFn, logger *slog.Logger) error {
	interval := cfg.Heartbeat
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if err := heartbeatExecutor(ctx, member.Name, provider, ctrl, logger); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, member.MaxConcurrent+1)
	go func() {
		for {
			sleepOrCancel(runCtx, interval)
			if runCtx.Err() != nil {
				errCh <- nil
				return
			}
			if err := heartbeatExecutor(runCtx, member.Name, provider, ctrl, logger); err != nil {
				errCh <- err
				return
			}
		}
	}()
	instanceID := time.Now().UnixNano()
	for slot := range member.MaxConcurrent {
		slot := slot
		go func() {
			runExecutorOfferSlot(runCtx, cfg, member, instanceID, slot, ctrl, ledger, exec, logger)
			errCh <- nil
		}()
	}
	err := <-errCh
	cancel()
	for range member.MaxConcurrent {
		<-errCh
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func heartbeatExecutor(ctx context.Context, executorName string, provider headroomProvider, ctrl executorMembershipClient, logger *slog.Logger) error {
	report := currentCapacity(ctx, provider)
	if report.headroom == nil {
		return errors.New("local admission daemon unavailable; executor heartbeat withheld")
	}
	heartbeatCtx, cancel := context.WithTimeout(ctx, poolHeartbeatTimeout)
	err := ctrl.HeartbeatExecutor(heartbeatCtx, executorName, *report.headroom)
	cancel()
	if err != nil {
		return fmt.Errorf("executor heartbeat: %w", err)
	}
	logger.Debug("executor liveness reported", "headroom_cores", report.headroom.Cores,
		"headroom_memory_bytes", report.headroom.MemoryBytes, "queue_depth", report.headroom.QueueDepth)
	return nil
}

func runExecutorOfferSlot(ctx context.Context, cfg AgentConfig, member AgentCoordinatorConfig, instanceID int64, slot int, ctrl executorMembershipClient, ledger ExecutorCapacityLedger, exec executorNodeFn, logger *slog.Logger) {
	limits, err := executorCapacityLimitsFor(cfg, member)
	if err != nil {
		logger.Error("executor capacity configuration is invalid", "err", err, "slot", slot)
		return
	}
	offerPoll := cfg.Poll
	if offerPoll <= 0 || offerPoll > 500*time.Millisecond {
		offerPoll = 500 * time.Millisecond
	}
	for ctx.Err() == nil {
		prepareCtx, cancelPrepare := context.WithTimeout(ctx, executorClaimRequestTimeout)
		preparation, err := ctrl.PrepareExecutorClaim(prepareCtx, member.Name)
		cancelPrepare()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("executor claim preparation failed", "err", err, "slot", slot)
				sleepOrCancel(ctx, cfg.Poll)
			}
			continue
		}
		if preparation == nil {
			sleepOrCancel(ctx, cfg.Poll)
			continue
		}
		if slot >= preparation.Membership.MaxConcurrent {
			sleepOrCancel(ctx, cfg.Poll)
			continue
		}
		reservation, err := ledger.Reserve(ctx, preparation.Summary, preparation.Membership, limits, slot)
		if err != nil {
			if !errors.Is(err, ErrExecutorCapacityUnavailable) && ctx.Err() == nil {
				logger.Error("executor capacity reservation failed", "err", err, "slot", slot)
			}
			sleepOrCancel(ctx, cfg.Poll)
			continue
		}
		holderID := fmt.Sprintf("executor:%s:%s:%d:%d", member.Name, preparation.Membership.MembershipID, instanceID, slot)
		claim := client.ExecutorClaim{
			ExecutorName: member.Name, HolderID: holderID,
			ReservationID: reservation.ID(), ResourceDigest: reservation.ResourceDigest(),
			Slot: reservation.Slot(), Lease: cfg.Lease,
		}
		reservationCtx, cancelReservation := reservation.ExecutionContext(ctx)
		offerStop := time.Now().Add(executorClaimRequestTimeout)
		if preparation.OfferDeadline != nil {
			offerStop = preparation.OfferDeadline.Add(executorOfferTransportBudget)
			minimumStop := time.Now().Add(executorOfferTransportBudget)
			if offerStop.Before(minimumStop) {
				offerStop = minimumStop
			}
		}
		offerCtx, cancelOffer := context.WithDeadline(reservationCtx, offerStop)
		won := false
		for offerCtx.Err() == nil {
			requestStop := time.Now().Add(executorClaimRequestTimeout)
			if offerStop.Before(requestStop) {
				requestStop = offerStop
			}
			requestCtx, cancelRequest := context.WithDeadline(offerCtx, requestStop)
			result, err := ctrl.OfferExecutorClaim(requestCtx, claim, preparation.Summary.RunID, preparation.Summary.NodeID)
			cancelRequest()
			if err != nil {
				if offerCtx.Err() == nil {
					logger.Error("executor claim offer failed", "err", err, "slot", slot)
					sleepOrCancel(offerCtx, offerPoll)
				}
				continue
			}
			if result.Node != nil {
				admission, err := reservation.Consume()
				if err != nil {
					logger.Error("awarded reservation is no longer runnable", "err", err,
						"run_id", result.Node.RunID, "node_id", result.Node.NodeID, "slot", slot)
					break
				}
				won = true
				logger.Info("executor claimed node", "run_id", result.Node.RunID, "node_id", result.Node.NodeID, "slot", slot)
				exec(reservationCtx, result.Node, holderID, admission)
				break
			}
			if !result.Pending {
				break
			}
			sleepOrCancel(offerCtx, offerPoll)
		}
		cancelOffer()
		cancelReservation()
		if err := reservation.Release(); err != nil && ctx.Err() == nil {
			logger.Error("executor capacity release failed", "err", err, "slot", slot)
		}
		if !won {
			sleepOrCancel(ctx, cfg.Poll)
		}
	}
}

func superviseAgentMembership(ctx context.Context, run func(context.Context) error, logger *slog.Logger) error {
	backoff := time.Second
	for {
		err := run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		logger.Error("executor membership stopped; retrying", "err", err, "backoff", backoff)
		sleepOrCancel(ctx, backoff)
		if ctx.Err() != nil {
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
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
	memberships := agentCoordinators(cfg)
	logger.Info(
		"sparkwing agent starting",
		"config", *configPath,
		"name", cfg.Name,
		"coordinators", len(memberships),
		"registered", cfg.registered,
		"labels", cfg.Labels,
		"max_concurrent", cfg.MaxConcurrent,
		"spawn_policy", cfg.SpawnPolicy,
		"observed_platform", executorinfo.DetectObservedPlatform(),
	)

	if !cfg.registered {
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
			CacheToken: cfg.CacheToken, Token: cfg.Token, HolderPrefix: prefix,
			Labels: cfg.Labels, MaxConcurrent: cfg.MaxConcurrent, PollInterval: cfg.Poll,
			Lease: cfg.Lease, HeartbeatInterval: cfg.Heartbeat, SourceName: "agent",
			LocalAdmission: cfg.LocalAdmission != nil && *cfg.LocalAdmission, LocalReserve: cfg.LocalReserve,
			Contribution: cfg.Contribution,
		}, logger)
	}

	ledger := NewWingdExecutorCapacityLedger("", "", logger)
	errCh := make(chan error, len(memberships))
	for _, membership := range memberships {
		membership := membership
		go func() {
			memberLogger := logger.With("coordinator", membership.Controller, "executor", membership.Name)
			errCh <- superviseAgentMembership(ctx, func(runCtx context.Context) error {
				return runAgentMembership(runCtx, cfg, membership, ledger, memberLogger)
			}, memberLogger)
		}()
	}
	for range memberships {
		if err := <-errCh; err != nil {
			return err
		}
	}
	return nil
}
