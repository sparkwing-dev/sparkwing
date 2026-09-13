package cluster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/internal/executorinfo"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func agentCoordinators(cfg agentconfig.Config) []agentconfig.Coordinator {
	if len(cfg.Coordinators) > 0 {
		return cfg.Coordinators
	}
	return []agentconfig.Coordinator{{
		Name:       cfg.Name,
		Controller: cfg.Controller, Logs: cfg.Logs, Gitcache: cfg.Gitcache,
		CacheToken: cfg.CacheToken, Profile: cfg.Profile, Token: cfg.Token,
		MaxConcurrent: cfg.MaxConcurrent, Contribution: cfg.Contribution,
	}}
}

func runAgentMembership(ctx context.Context, cfg agentconfig.Config, member agentconfig.Coordinator, ledger ExecutorCapacityLedger, logger *slog.Logger) error {
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

func executorCapacityLimitsFor(cfg agentconfig.Config, member agentconfig.Coordinator) (executorCapacityLimits, error) {
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

func runAgentMembershipLoop(ctx context.Context, cfg agentconfig.Config, member agentconfig.Coordinator, provider headroomProvider, ctrl executorMembershipClient, ledger ExecutorCapacityLedger, exec executorNodeFn, logger *slog.Logger) error {
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

func runExecutorOfferSlot(ctx context.Context, cfg agentconfig.Config, member agentconfig.Coordinator, instanceID int64, slot int, ctrl executorMembershipClient, ledger ExecutorCapacityLedger, exec executorNodeFn, logger *slog.Logger) {
	limits, err := executorCapacityLimitsFor(cfg, member)
	if err != nil {
		logger.Error("executor capacity configuration is invalid", "err", err, "slot", slot)
		return
	}
	offerPoll := cfg.Poll
	if offerPoll <= 0 || offerPoll > 500*time.Millisecond {
		offerPoll = 500 * time.Millisecond
	}
	shed := newShedLog(shedWarnInterval)
	for ctx.Err() == nil {
		prepareCtx, cancelPrepare := context.WithTimeout(ctx, executorClaimRequestTimeout)
		preparationSink := executionpolicy.NewPreparationSink()
		prepareCtx = executionpolicy.WithPreparationSink(prepareCtx, preparationSink)
		preparation, err := ctrl.PrepareExecutorClaim(prepareCtx, member.Name)
		cancelPrepare()
		if err != nil {
			if ctx.Err() == nil {
				if wait, ok := unavailableBackoff(err, cfg.Poll); ok {
					logger.Debug("executor claim preparation shed by the controller; backing off",
						"err", err, "retry_after", wait, "slot", slot)
					if shed.due() {
						logger.Warn("controller is shedding executor claims; polling more slowly",
							"err", err, "retry_after", wait, "slot", slot)
					}
					sleepOrCancel(ctx, wait)
					continue
				}
				logger.Error("executor claim preparation failed", "err", err, "slot", slot)
				sleepOrCancel(ctx, cfg.Poll)
			}
			continue
		}
		if preparation == nil {
			sleepOrCancel(ctx, cfg.Poll)
			continue
		}
		binding := preparationSink.Load()
		if binding.IsZero() {
			logger.Error("executor claim preparation omitted its sealed execution binding", "slot", slot)
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
			requestCtx, err = executionpolicy.WithOfferBinding(requestCtx, binding)
			if err != nil {
				cancelRequest()
				logger.Error("executor claim binding is invalid", "err", err, "slot", slot)
				break
			}
			result, err := ctrl.OfferExecutorClaim(requestCtx, claim, preparation.Summary.RunID, preparation.Summary.NodeID)
			cancelRequest()
			if err != nil {
				if offerCtx.Err() == nil {
					if wait, ok := unavailableBackoff(err, offerPoll); ok {
						logger.Debug("executor claim offer shed by the controller; backing off",
							"err", err, "retry_after", wait, "slot", slot)
						if shed.due() {
							logger.Warn("controller is shedding executor claims; polling more slowly",
								"err", err, "retry_after", wait, "slot", slot)
						}
						sleepOrCancel(offerCtx, wait)
						continue
					}
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

func RunAgentCLI(args []string) error {
	return runAgentCLI(args, buildinfo.Read("sparkwing-runner", ""))
}

func runAgentCLI(args []string, identity buildinfo.Identity) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "", "path to agent.yaml (default: ~/.config/sparkwing/agent.yaml)")
	allowEnrolledPreview := fs.Bool("allow-enrolled-preview", false,
		"start an enrolled configuration against the unfinished enrolled execution path")
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
	if err := agentconfig.CheckEnrolledExecutionAvailable(cfg, *allowEnrolledPreview); err != nil {
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
		"registered", cfg.Enrolled(),
		"labels", cfg.Labels,
		"max_concurrent", cfg.MaxConcurrent,
		"spawn_policy", cfg.SpawnPolicy,
		"observed_platform", executorinfo.DetectObservedPlatform(),
	)

	if !cfg.Enrolled() {
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
	runtimeCtx, err := executionpolicy.WithRuntimeReport(ctx, executionpolicy.CurrentRuntimeReport(identity))
	if err != nil {
		return err
	}

	ledger := NewWingdExecutorCapacityLedger("", "", logger)
	errCh := make(chan error, len(memberships))
	for _, membership := range memberships {
		membership := membership
		go func() {
			memberLogger := logger.With("coordinator", membership.Controller, "executor", membership.Name)
			errCh <- superviseAgentMembership(runtimeCtx, func(runCtx context.Context) error {
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
