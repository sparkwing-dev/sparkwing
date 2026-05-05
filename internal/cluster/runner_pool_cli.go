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
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
)

// PoolLoopConfig is the parameter set shared by every caller of
// RunPoolLoop: the in-cluster warm pool pod (`wing runner`), the
// off-cluster laptop agent (`sparkwing agent`), and anything else
// that wants to claim node work off the controller queue and execute
// it in-process via orchestrator.RunNodeOnce. Flag / YAML parsing lives
// in the respective CLI entry points; the loop itself is flag-agnostic.
type PoolLoopConfig struct {
	ControllerURL     string        // required
	LogsURL           string        // optional; empty = stdout only
	Token             string        // optional; empty = no auth header
	HolderPrefix      string        // e.g. "runner:hostname" or "agent:hostname"
	Labels            []string      // advertised to the controller's claim SQL
	MaxConcurrent     int           // in-flight claims; <=0 treated as 1
	PollInterval      time.Duration // back-off when the claim queue is empty
	Lease             time.Duration // initial claim lease granted per claim
	HeartbeatInterval time.Duration // 0 = lease/3
	// MaxClaims bounds how many successful claims the loop will dispatch
	// before returning nil. 0 = unlimited (laptop-agent default -- an
	// agent with no kubelet supervisor should not silently stop accepting
	// work). The in-cluster `wing runner` sets this to 25 so the kubelet
	// restarts the container periodically, shedding accumulated PVC state
	// and any in-process drift.
	MaxClaims int
	// SourceName is the human-readable label used in log lines
	// ("pool runner", "agent"). Lets operators distinguish the two
	// shapes at a glance in mixed log output.
	SourceName string
}

// nodeClaimer is the narrow subset of controller-client methods
// runPoolLoop needs to claim work. Extracted as an interface so
// tests can drive the loop with a stub without spinning up an HTTP
// server or the full client.
type nodeClaimer interface {
	ClaimNode(ctx context.Context, holderID string, labels []string, lease time.Duration) (*store.Node, error)
}

// poolExecFn is the per-claim executor. The real implementation
// (executePooledNode) runs the node to terminal state while
// heartbeating; tests swap in a no-op to exercise the claim-counting
// machinery without pulling in orchestrator.RunNodeOnce.
type poolExecFn func(ctx context.Context, n *store.Node, holderID string)

// RunPoolLoop is the claim / execute / heartbeat loop shared by
// `wing runner` (in-cluster warm pool) and `sparkwing agent` (laptop
// agent). Blocks until ctx is cancelled or MaxClaims is reached.
func RunPoolLoop(ctx context.Context, cfg PoolLoopConfig, logger *slog.Logger) error {
	if cfg.ControllerURL == "" {
		return errors.New("pool loop: ControllerURL is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = normalizePoolLoopConfig(cfg)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctrl := client.NewWithToken(cfg.ControllerURL, httpClient, cfg.Token)

	exec := func(execCtx context.Context, n *store.Node, holderID string) {
		executePooledNode(execCtx, ctrl, cfg.ControllerURL, cfg.LogsURL, cfg.Token,
			n, holderID, cfg.Lease, cfg.HeartbeatInterval, cfg.SourceName, logger)
	}
	return runPoolLoop(ctx, cfg, ctrl, exec, logger)
}

// normalizePoolLoopConfig fills defaults. Split out so the testable
// runPoolLoop and the real RunPoolLoop share one definition of
// "what's a valid cfg".
func normalizePoolLoopConfig(cfg PoolLoopConfig) PoolLoopConfig {
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.Lease <= 0 {
		cfg.Lease = store.DefaultLeaseDuration
	}
	if cfg.SourceName == "" {
		cfg.SourceName = "pool runner"
	}
	if cfg.HolderPrefix == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			cfg.HolderPrefix = "runner:" + h
		} else {
			cfg.HolderPrefix = "runner"
		}
	}
	return cfg
}

// runPoolLoop is the testable core. cfg must already be normalized.
// claimer + exec are injected so tests don't need an HTTP stack.
func runPoolLoop(ctx context.Context, cfg PoolLoopConfig, claimer nodeClaimer, exec poolExecFn, logger *slog.Logger) error {
	logger.Info(cfg.SourceName+" started",
		"controller", cfg.ControllerURL,
		"logs", cfg.LogsURL,
		"max_concurrent", cfg.MaxConcurrent,
		"max_claims", cfg.MaxClaims,
		"poll", cfg.PollInterval,
		"holder_prefix", cfg.HolderPrefix,
		"labels", cfg.Labels,
		"auth", cfg.Token != "",
	)

	sem := make(chan struct{}, cfg.MaxConcurrent)
	var wg sync.WaitGroup
	defer wg.Wait()

	claimed := 0
	for {
		if err := ctx.Err(); err != nil {
			logger.Info(cfg.SourceName+" shutting down", "reason", err)
			return nil
		}
		// MaxClaims bound: count only successful claims ("claimed"
		// outcomes). Empty polls and transient claim errors don't
		// tick the counter; otherwise an empty queue could churn
		// the pod through kubelet backoff without having done any
		// real work.
		if cfg.MaxClaims > 0 && claimed >= cfg.MaxClaims {
			logger.Info(cfg.SourceName+" max-claims reached; exiting for container restart",
				"claimed", claimed, "max_claims", cfg.MaxClaims)
			return nil
		}

		// Block until we have a free execution slot. Leaves work on
		// the controller queue for another runner to claim under load.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}

		holderID := fmt.Sprintf("%s:%d", cfg.HolderPrefix, time.Now().UnixNano())
		n, err := claimer.ClaimNode(ctx, holderID, cfg.Labels, cfg.Lease)
		if err != nil {
			<-sem
			if errors.Is(err, context.Canceled) {
				return nil
			}
			observeClaimOutcome("error")
			logger.Error("claim failed", "err", err, "source", cfg.SourceName)
			sleepOrCancel(ctx, cfg.PollInterval)
			continue
		}
		if n == nil {
			<-sem
			observeClaimOutcome("empty")
			sleepOrCancel(ctx, cfg.PollInterval)
			continue
		}
		observeClaimOutcome("claimed")
		claimed++

		logger.Info("claimed node",
			"run_id", n.RunID, "node_id", n.NodeID,
			"holder", holderID, "source", cfg.SourceName)

		wg.Add(1)
		go func(n *store.Node, holderID string) {
			defer wg.Done()
			defer func() { <-sem }()
			exec(ctx, n, holderID)
		}(n, holderID)
	}
}

// runRunnerCLI implements `wing runner` -- the long-lived warm pool
// runner pod. Thin CLI wrapper around RunPoolLoop.
func runRunnerCLI(args []string) error {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"),
		"controller base URL (required)")
	logsURL := fs.String("logs", os.Getenv("SPARKWING_LOGS_URL"),
		"logs service URL (optional; pod stdout if empty)")
	poll := fs.Duration("poll", 500*time.Millisecond,
		"poll interval when the claim queue is empty")
	heartbeat := fs.Duration("heartbeat", 0,
		"per-claim heartbeat cadence (default: 3s)")
	maxConcurrent := fs.Int("max-concurrent", 1,
		"max nodes this runner will execute in parallel")
	lease := fs.Duration("lease", store.DefaultLeaseDuration,
		"initial claim lease the controller grants on each claim")
	holderPrefix := fs.String("holder-prefix", "",
		"holder id prefix (defaults to HOSTNAME or 'runner')")
	var labels multiFlag
	fs.Var(&labels, "label",
		"runner label (repeatable, e.g. --label=arm64 --label=arch=arm64)")
	token := fs.String("token", os.Getenv("SPARKWING_AGENT_TOKEN"),
		"shared-secret bearer token for controller + logs auth (env: SPARKWING_AGENT_TOKEN)")
	metricsAddr := fs.String("metrics-addr", ":9090",
		"address for the /metrics listener (empty disables)")
	maxClaims := fs.Int("max-claims-before-restart", 25,
		"exit the loop after N successful claims so kubelet restarts the container (0 = unlimited; FOLLOWUPS #12)")
	alsoClaimTriggers := fs.Bool("also-claim-triggers", false,
		"run the trigger-loop (claim triggers, clone repo, compile, exec handle-trigger) as a goroutine alongside the node-claim loop. Lets one warm-runner pool handle both trigger and node layers.")
	gitcacheURL := fs.String("gitcache", os.Getenv("SPARKWING_GITCACHE_URL"),
		"sparkwing-cache URL for the trigger-loop (required when --also-claim-triggers is set)")
	triggerSources := fs.String("trigger-sources", "",
		"comma-separated trigger_source values the trigger loop handles (e.g. github); empty = accept any source")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *controllerURL == "" {
		fs.Usage()
		return errors.New("--controller is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-warm-runner"})
	defer tel.Shutdown(context.Background())

	logger := slog.Default()
	go func() {
		if err := StartMetricsListener(ctx, *metricsAddr, logger); err != nil {
			logger.Error("metrics listener failed", "err", err)
		}
	}()

	// FOLLOWUPS #12: prune stale state from the warm PVC before the
	// claim loop begins. Time-boxed inside GCWarmRoot; skip-on-timeout
	// rather than fail-closed so a stuck sweep doesn't block claims.
	if paths, perr := orchestrator.DefaultPaths(); perr == nil {
		ctrl := client.NewWithToken(*controllerURL, nil, *token)
		stats, err := orchestrator.GCWarmRoot(ctx, paths.Root, ctrl, logger)
		if err != nil {
			logger.Warn("gc: warm sweep returned error (continuing)", "err", err)
		} else {
			logger.Info("gc: warm sweep complete",
				"git_dirs", stats.GitDirsRemoved,
				"tmp_entries", stats.TmpEntriesRemoved,
				"run_dirs", stats.RunDirsRemoved,
				"bytes_freed", stats.BytesFreed,
			)
		}
	}

	if *alsoClaimTriggers {
		if *gitcacheURL == "" {
			return errors.New("--also-claim-triggers requires --gitcache or SPARKWING_GITCACHE_URL")
		}
		go func() {
			if err := RunTriggerLoop(ctx, TriggerLoopOptions{
				ControllerURL: *controllerURL,
				LogsURL:       *logsURL,
				GitcacheURL:   *gitcacheURL,
				Token:         *token,
				Poll:          *poll,
				Logger:        slog.Default().With("loop", "trigger"),
				Sources:       splitCSV(*triggerSources),
			}); err != nil {
				slog.Default().Error("trigger loop exited with error", "err", err)
			}
		}()
	}

	return RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		Token:             *token,
		HolderPrefix:      *holderPrefix,
		Labels:            []string(labels),
		MaxConcurrent:     *maxConcurrent,
		PollInterval:      *poll,
		Lease:             *lease,
		HeartbeatInterval: *heartbeat,
		MaxClaims:         *maxClaims,
		SourceName:        "pool runner",
	}, slog.Default())
}

// executePooledNode runs one claimed node to terminal state. Spawns a
// heartbeat goroutine so the claim lease stays alive for the life of
// the execution, cancels it on return.
func executePooledNode(
	ctx context.Context,
	ctrl *client.Client,
	controllerURL, logsURL, token string,
	n *store.Node,
	holderID string,
	lease, hbInterval time.Duration,
	source string,
	logger *slog.Logger,
) {
	if hbInterval <= 0 {
		hbInterval = poolHeartbeatDefaultInterval
	}
	if hbInterval < 200*time.Millisecond {
		hbInterval = 200 * time.Millisecond
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		runPoolHeartbeat(execCtx, ctrl, n.RunID, n.NodeID, holderID, lease, hbInterval, cancel, source, logger)
	}()

	res, err := orchestrator.RunNodeOnce(execCtx, controllerURL, logsURL, n.RunID, n.NodeID, holderID, token,
		&stdoutLogger{}, logger)
	cancel()
	hbWG.Wait()

	if err != nil {
		// Setup-level failure (pipeline missing, plan rebuild failed).
		// InProcessRunner never ran, so no terminal state was written.
		// Best effort: the orchestrator will time out on GetNode
		// polling and either the reaper or an operator reconciles.
		logger.Error(source+" setup failure",
			"run_id", n.RunID, "node_id", n.NodeID, "err", err)
		return
	}
	logger.Info(source+" finished node",
		"run_id", n.RunID, "node_id", n.NodeID, "outcome", res.Outcome)
}

// Timing knobs for the node-claim heartbeat. Vars (not consts) so
// tests can shrink them to millisecond-scale. Semantics match the
// trigger heartbeat: a single successful tick extends the lease by
// its full duration; silence beyond the lease window cancels the
// node work; ErrLockHeld (reaper flipped the claim) also cancels.
var (
	// poolHeartbeatDefaultInterval is the cadence when the caller
	// passes hbInterval <= 0. Mirrors triggerHeartbeatInterval for
	// symmetric behavior across the two claim layers.
	poolHeartbeatDefaultInterval = 3 * time.Second

	// poolHeartbeatTimeout is the per-call HTTP timeout. Strictly
	// less than poolHeartbeatDefaultInterval.
	poolHeartbeatTimeout = 2 * time.Second

	// poolHeartbeatMaxSilence bounds how long node work keeps
	// running without successful controller contact. At this point
	// the controller's reaper has almost certainly flipped our
	// claim; continuing to execute would race its subsequent
	// claimer and produce duplicate node-terminal writes.
	poolHeartbeatMaxSilence = 3 * time.Minute
)

// runPoolHeartbeat keeps the claim lease fresh until ctx cancels.
// On terminal signals -- ErrLockHeld (reaper flipped our claim) or
// ≥3min of consecutive heartbeat failures -- it invokes killNode
// to cancel the in-flight node execution and returns. Transient
// failures are logged but absorbed: the 3min lease window gives
// plenty of headroom for a cold GC or controller blip.
func runPoolHeartbeat(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID, holderID string,
	lease, interval time.Duration,
	killNode context.CancelFunc,
	source string,
	logger *slog.Logger,
) {
	t := time.NewTicker(interval)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(ctx, poolHeartbeatTimeout)
			err := ctrl.HeartbeatNodeClaim(hbCtx, runID, nodeID, holderID, lease)
			cancel()
			if err == nil {
				lastOK = time.Now()
				continue
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, store.ErrLockHeld) {
				logger.Error(source+" heartbeat: claim reaped by controller; cancelling node",
					"run_id", runID, "node_id", nodeID)
				killNode()
				return
			}
			silence := time.Since(lastOK)
			if silence >= poolHeartbeatMaxSilence {
				logger.Error(source+" heartbeat: controller unreachable beyond lease window; cancelling node",
					"run_id", runID, "node_id", nodeID,
					"silence", silence.Round(time.Second),
					"err", err)
				killNode()
				return
			}
			logger.Warn(source+" heartbeat failed",
				"run_id", runID, "node_id", nodeID,
				"err", err,
				"silence", silence.Round(time.Second))
		}
	}
}
