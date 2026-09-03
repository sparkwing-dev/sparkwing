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

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type PoolLoopConfig struct {
	ControllerURL     string
	LogsURL           string
	GitcacheURL       string
	CacheToken        string
	Token             string
	HolderPrefix      string
	Labels            []string
	ClaimPriority     int
	WorkerID          string
	ExecutorKind      string
	MaxConcurrent     int
	PollInterval      time.Duration
	Lease             time.Duration
	HeartbeatInterval time.Duration

	MaxClaims int

	SourceName string

	LocalAdmission bool

	LocalReserve string

	Home string

	Version string
}

type nodeClaimer interface {
	PrepareNodeClaim(ctx context.Context, executor client.NodeClaimExecutor) (*store.NodeSchedulingSummary, error)
	OfferNodeClaim(ctx context.Context, executor client.NodeClaimExecutor, runID, nodeID string) (client.NodeClaimOfferResult, error)
}

type poolReservation interface {
	ID() string
	Release()
	Watch(context.CancelFunc)
}

type poolExecFn func(ctx context.Context, n *store.Node, holderID string, reservation poolReservation)

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

	var admission *orchestrator.LocalAdmission
	var provider headroomProvider
	if cfg.LocalAdmission {
		rv, err := parseReserve(cfg.LocalReserve)
		if err != nil {
			return fmt.Errorf("pool loop: local reserve: %w", err)
		}
		admission = &orchestrator.LocalAdmission{
			Home:    cfg.Home,
			Version: cfg.Version,
			Origin:  wingwire.OriginController,
		}
		provider = newHeadroomProvider(cfg.Home, cfg.Version, rv)
		logger.Info("local admission engaged; controller work shares the local daemon",
			"reserve", cfg.LocalReserve, "source", cfg.SourceName)
	}

	reserve := func(reserveCtx context.Context, summary store.NodeSchedulingSummary, reservationID string) (poolReservation, bool, error) {
		if admission == nil {
			return processSlotReservation{id: reservationID}, true, nil
		}
		return admission.TryReserveFleetNode(reserveCtx, summary, reservationID)
	}
	exec := func(execCtx context.Context, n *store.Node, holderID string, reservation poolReservation) {
		executePooledNode(execCtx, ctrl, cfg.ControllerURL, cfg.LogsURL, cfg.GitcacheURL, cfg.Token, cfg.CacheToken,
			n, holderID, cfg.Lease, cfg.HeartbeatInterval, cfg.SourceName, logger, reservation, provider)
	}
	return runPoolLoop(ctx, cfg, ctrl, exec, reserve, provider, logger)
}

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
	if cfg.WorkerID == "" {
		cfg.WorkerID = cfg.HolderPrefix
	}
	if cfg.ExecutorKind == "" {
		cfg.ExecutorKind = "direct"
	}
	return cfg
}

type reserveNodeFn func(context.Context, store.NodeSchedulingSummary, string) (poolReservation, bool, error)

type processSlotReservation struct{ id string }

func (r processSlotReservation) ID() string             { return r.id }
func (processSlotReservation) Release()                 {}
func (processSlotReservation) Watch(context.CancelFunc) {}

func runPoolLoop(ctx context.Context, cfg PoolLoopConfig, claimer nodeClaimer, exec poolExecFn, reserve reserveNodeFn, provider headroomProvider, logger *slog.Logger) error {
	logger.Info(
		cfg.SourceName+" started",
		"controller", cfg.ControllerURL,
		"logs", cfg.LogsURL,
		"max_concurrent", cfg.MaxConcurrent,
		"max_claims", cfg.MaxClaims,
		"poll", cfg.PollInterval,
		"holder_prefix", cfg.HolderPrefix,
		"labels", cfg.Labels,
		"claim_priority", cfg.ClaimPriority,
		"executor_kind", cfg.ExecutorKind,
		"auth", cfg.Token != "",
	)

	instanceID := time.Now().UnixNano()
	budget := &poolClaimBudget{limit: cfg.MaxClaims}
	var wg sync.WaitGroup
	for slot := range cfg.MaxConcurrent {
		holderID := fmt.Sprintf("%s:%d:%d", cfg.HolderPrefix, instanceID, slot)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runPoolSlot(ctx, cfg, holderID, claimer, exec, reserve, provider, budget, logger)
		}()
	}
	wg.Wait()
	logger.Info(cfg.SourceName+" shutting down", "reason", context.Cause(ctx), "claimed", budget.claimedCount())
	return nil
}

type poolClaimBudget struct {
	mu       sync.Mutex
	limit    int
	claimed  int
	inflight int
}

func (b *poolClaimBudget) reserve() (ok, done bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && b.claimed >= b.limit {
		return false, true
	}
	if b.limit > 0 && b.claimed+b.inflight >= b.limit {
		return false, false
	}
	b.inflight++
	return true, false
}

func (b *poolClaimBudget) finish(claimed bool) {
	b.mu.Lock()
	b.inflight--
	if claimed {
		b.claimed++
	}
	b.mu.Unlock()
}

func (b *poolClaimBudget) claimedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.claimed
}

func runPoolSlot(ctx context.Context, cfg PoolLoopConfig, holderID string, claimer nodeClaimer, exec poolExecFn, reserve reserveNodeFn, provider headroomProvider, budget *poolClaimBudget, logger *slog.Logger) {
	for ctx.Err() == nil {
		ok, done := budget.reserve()
		if done {
			return
		}
		if !ok {
			sleepOrCancel(ctx, cfg.PollInterval)
			continue
		}
		claimed := false
		func() {
			defer func() { budget.finish(claimed) }()
			executor := client.NodeClaimExecutor{
				HolderID:      holderID,
				WorkerID:      cfg.WorkerID,
				ExecutorKind:  cfg.ExecutorKind,
				ClaimPriority: cfg.ClaimPriority,
				Labels:        cfg.Labels,
				Lease:         cfg.Lease,
				Headroom:      currentHeadroom(ctx, provider),
			}
			summary, err := claimer.PrepareNodeClaim(ctx, executor)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					observeClaimOutcome("error")
					logger.Error("claim preparation failed", "err", err, "source", cfg.SourceName)
				}
				return
			}
			if summary == nil {
				observeClaimOutcome("empty")
				return
			}
			reservationID := fmt.Sprintf("%s:%s:%s", holderID, summary.RunID, summary.NodeID)
			reservation, available, err := reserve(ctx, *summary, reservationID)
			if err != nil {
				observeClaimOutcome("error")
				logger.Error("claim reservation failed", "err", err, "source", cfg.SourceName)
				return
			}
			if !available {
				observeClaimOutcome("reserved")
				return
			}
			defer func() {
				if !claimed {
					reservation.Release()
				}
			}()
			executor.ReservationID = reservation.ID()
			for ctx.Err() == nil {
				executor.Headroom = currentHeadroom(ctx, provider)
				result, err := claimer.OfferNodeClaim(ctx, executor, summary.RunID, summary.NodeID)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						observeClaimOutcome("error")
						logger.Error("claim offer failed", "err", err, "source", cfg.SourceName)
					}
					return
				}
				if result.Node != nil {
					claimed = true
					observeClaimOutcome("claimed")
					logger.Info("claimed node", "run_id", result.Node.RunID, "node_id", result.Node.NodeID,
						"holder", holderID, "source", cfg.SourceName)
					exec(ctx, result.Node, holderID, reservation)
					return
				}
				if !result.Pending {
					observeClaimOutcome("empty")
					return
				}
				sleepOrCancel(ctx, cfg.PollInterval)
			}
		}()
		if !claimed {
			sleepOrCancel(ctx, cfg.PollInterval)
		}
	}
}

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
	claimPriority := fs.Int("claim-priority", 0,
		"executor priority from 0 through 100; registered controller policy may lower it")
	lease := fs.Duration("lease", store.DefaultLeaseDuration,
		"initial claim lease to request on each claim; the controller clamps it to 10m")
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
	claimNodes := fs.Bool("claim-nodes", true,
		"claim and execute controller node work in this runner process")
	gitcacheURL := fs.String("gitcache", os.Getenv("SPARKWING_GITCACHE_URL"),
		"sparkwing-cache URL for the trigger-loop (required when --also-claim-triggers is set)")
	triggerSources := fs.String("trigger-sources", "",
		"comma-separated trigger_source values the trigger loop handles (e.g. github); empty = accept any source")
	triggerRunnerKind := fs.String("trigger-runner", os.Getenv("SPARKWING_TRIGGER_RUNNER"),
		"node runner used by claimed triggers: inprocess | k8s | warm")
	triggerRunnerNamespace := fs.String("trigger-runner-namespace", os.Getenv("POD_NAMESPACE"),
		"namespace for trigger-spawned runner Jobs (k8s or warm fallback)")
	triggerRunnerImage := fs.String("trigger-runner-image", os.Getenv("SPARKWING_RUNNER_IMAGE"),
		"runner image for trigger-spawned runner Jobs (k8s or warm fallback)")
	triggerRunnerSA := fs.String("trigger-runner-sa", os.Getenv("SPARKWING_RUNNER_SA"),
		"service account for trigger-spawned runner Jobs (k8s or warm fallback)")
	triggerRunnerPullSecret := fs.String("trigger-runner-image-pull-secret", os.Getenv("SPARKWING_IMAGE_PULL_SECRET"),
		"imagePullSecret for trigger-spawned runner Jobs (k8s or warm fallback)")
	triggerRunnerCtrlURL := fs.String("trigger-runner-controller-url", os.Getenv("SPARKWING_RUNNER_CONTROLLER_URL"),
		"controller URL for trigger-spawned runner Jobs (defaults to --controller)")
	triggerRunnerLogsURL := fs.String("trigger-runner-logs-url", os.Getenv("SPARKWING_RUNNER_LOGS_URL"),
		"logs-service URL for trigger-spawned runner Jobs (defaults to --logs)")
	triggerRunnerKubeconfig := fs.String("trigger-runner-kubeconfig", os.Getenv("KUBECONFIG"),
		"kubeconfig path for creating trigger-spawned Jobs (empty = in-cluster)")
	triggerArtifactStore := fs.String("trigger-artifact-store", os.Getenv("SPARKWING_CACHE_URL"),
		"artifact/cache store URL passed to trigger-spawned runner Jobs")
	triggerDependencyProxy := fs.String("dependency-proxy", os.Getenv("SPARKWING_DEPENDENCY_PROXY_URL"),
		"base URL of the in-cluster pull-through package proxy stamped on trigger-spawned runner Jobs as "+
			"GOPROXY / npm_config_registry / PIP_INDEX_URL; empty derives it from --gitcache, \"off\" disables "+
			"(env: SPARKWING_DEPENDENCY_PROXY_URL)")
	triggerRunnerPullPolicy := fs.String("trigger-runner-image-pull-policy", os.Getenv("SPARKWING_IMAGE_PULL_POLICY"),
		"imagePullPolicy for trigger-spawned runner Jobs: Always | IfNotPresent | Never "+
			"(default IfNotPresent; env: SPARKWING_IMAGE_PULL_POLICY)")
	var triggerRunnerNodeSelector multiFlag = splitCSV(os.Getenv("SPARKWING_RUNNER_NODE_SELECTOR"))
	fs.Var(&triggerRunnerNodeSelector, "trigger-runner-node-selector",
		"node selector for trigger-spawned runner Jobs, key=value (repeatable; env: SPARKWING_RUNNER_NODE_SELECTOR)")
	var triggerRunnerTolerations multiFlag = splitCSV(os.Getenv("SPARKWING_RUNNER_TOLERATION"))
	fs.Var(&triggerRunnerTolerations, "trigger-runner-toleration",
		"toleration for trigger-spawned runner Jobs, key[=value]:Effect (repeatable; env: SPARKWING_RUNNER_TOLERATION)")
	localAdmission := fs.Bool("local-admission", false,
		"route claimed nodes through this box's local admission daemon (for a runner on a box that also runs local pipelines; off for in-cluster pods)")
	localReserve := fs.String("local-reserve", os.Getenv("SPARKWING_LOCAL_RESERVE"),
		"host capacity held back from advertised headroom in the daemon budget grammar, e.g. 2,4gb or 10% (env: SPARKWING_LOCAL_RESERVE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *controllerURL == "" {
		fs.Usage()
		return errors.New("--controller is required")
	}
	if *claimPriority < 0 || *claimPriority > 100 {
		return fmt.Errorf("--claim-priority=%d: expected 0 through 100", *claimPriority)
	}
	if *triggerRunnerKind != "" && *triggerRunnerKind != "inprocess" &&
		*triggerRunnerKind != "k8s" && *triggerRunnerKind != "warm" {
		return fmt.Errorf("--trigger-runner=%q: expected inprocess, k8s, or warm", *triggerRunnerKind)
	}
	if (*triggerRunnerKind == "k8s" || *triggerRunnerKind == "warm") && !*alsoClaimTriggers {
		return errors.New("--trigger-runner=k8s or warm requires --also-claim-triggers")
	}
	if *triggerRunnerKind == "warm" && *claimNodes {
		return errors.New("--trigger-runner=warm requires --claim-nodes=false so this process does not race remote agents")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-warm-runner"})
	defer func() { _ = tel.Shutdown(context.Background()) }()

	logger := slog.Default()
	go func() {
		if err := StartMetricsListener(ctx, *metricsAddr, logger); err != nil {
			logger.Error("metrics listener failed", "err", err)
		}
	}()

	if paths, perr := orchestrator.DefaultPaths(); perr == nil {
		ctrl := client.NewWithToken(*controllerURL, nil, *token)
		stats, err := orchestrator.GCWarmRoot(ctx, paths.Root, ctrl, logger)
		if err != nil {
			logger.Warn("gc: warm sweep returned error (continuing)", "err", err)
		} else {
			logger.Info(
				"gc: warm sweep complete",
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
		// safety: without this the child rejects each claimed trigger after admission.
		usesK8sJobs := *triggerRunnerKind == "k8s" ||
			(*triggerRunnerKind == "warm" && *triggerRunnerImage != "")
		if usesK8sJobs && *triggerRunnerSA == "" {
			return fmt.Errorf("--trigger-runner-sa (or SPARKWING_RUNNER_SA) is required with --trigger-runner=%s", *triggerRunnerKind)
		}
		go func() {
			if err := RunTriggerLoop(ctx, TriggerLoopOptions{
				ControllerURL:   *controllerURL,
				LogsURL:         *logsURL,
				GitcacheURL:     *gitcacheURL,
				Token:           *token,
				RunnerKind:      *triggerRunnerKind,
				K8sNamespace:    *triggerRunnerNamespace,
				K8sImage:        *triggerRunnerImage,
				K8sRunnerSA:     *triggerRunnerSA,
				K8sPullSecret:   *triggerRunnerPullSecret,
				K8sCtrlURL:      firstNonEmpty(*triggerRunnerCtrlURL, *controllerURL),
				K8sLogsURL:      firstNonEmpty(*triggerRunnerLogsURL, *logsURL),
				Kubeconfig:      *triggerRunnerKubeconfig,
				ArtifactStore:   *triggerArtifactStore,
				K8sNodeSelector: triggerRunnerNodeSelector,
				K8sTolerations:  triggerRunnerTolerations,

				DependencyProxy:    k8srunner.ResolveDependencyProxy(*triggerDependencyProxy, *gitcacheURL),
				K8sImagePullPolicy: *triggerRunnerPullPolicy,
				Poll:               *poll,
				Logger:             slog.Default().With("loop", "trigger"),
				Sources:            splitCSV(*triggerSources),
			}); err != nil {
				slog.Default().Error("trigger loop exited with error", "err", err)
			}
		}()
	}
	if !*claimNodes {
		if !*alsoClaimTriggers {
			return errors.New("--claim-nodes=false requires --also-claim-triggers")
		}
		<-ctx.Done()
		return nil
	}

	return RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		GitcacheURL:       *gitcacheURL,
		CacheToken:        os.Getenv("SPARKWING_CACHE_TOKEN"),
		Token:             *token,
		HolderPrefix:      *holderPrefix,
		Labels:            []string(labels),
		ClaimPriority:     *claimPriority,
		WorkerID:          *holderPrefix,
		ExecutorKind:      "pool",
		MaxConcurrent:     *maxConcurrent,
		PollInterval:      *poll,
		Lease:             *lease,
		HeartbeatInterval: *heartbeat,
		MaxClaims:         *maxClaims,
		SourceName:        "pool runner",
		LocalAdmission:    *localAdmission,
		LocalReserve:      *localReserve,
	}, slog.Default())
}

func currentHeadroom(ctx context.Context, provider headroomProvider) *client.Headroom {
	if provider == nil {
		return nil
	}
	return provider(ctx)
}

func executePooledNode(
	ctx context.Context,
	ctrl *client.Client,
	controllerURL, logsURL, gitcacheURL, token, cacheToken string,
	n *store.Node,
	holderID string,
	lease, hbInterval time.Duration,
	source string,
	logger *slog.Logger,
	reservation poolReservation,
	provider headroomProvider,
) {
	if hbInterval <= 0 {
		hbInterval = poolHeartbeatDefaultInterval
	}
	if hbInterval < 200*time.Millisecond {
		hbInterval = 200 * time.Millisecond
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer reservation.Release()
	go reservation.Watch(cancel)

	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		runPoolHeartbeat(execCtx, ctrl, n.RunID, n.NodeID, holderID, lease, hbInterval, cancel, source, provider, logger)
	}()

	res, err := orchestrator.RunNodeOnce(execCtx, controllerURL, logsURL, n.RunID, n.NodeID, holderID, token,
		&stdoutLogger{}, logger, nil, orchestrator.WithGitcache(gitcacheURL, cacheToken))
	cancel()
	hbWG.Wait()

	if err != nil {
		logger.Error(source+" setup failure",
			"run_id", n.RunID, "node_id", n.NodeID, "err", err)
		return
	}
	logger.Info(source+" finished node",
		"run_id", n.RunID, "node_id", n.NodeID, "outcome", res.Outcome)
}

var (
	poolHeartbeatDefaultInterval = 3 * time.Second

	poolHeartbeatTimeout = 2 * time.Second

	poolHeartbeatMaxSilence = 3 * time.Minute
)

func runPoolHeartbeat(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID, holderID string,
	lease, interval time.Duration,
	killNode context.CancelFunc,
	source string,
	provider headroomProvider,
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
			err := ctrl.HeartbeatNodeClaim(hbCtx, runID, nodeID, holderID, lease, currentHeadroom(hbCtx, provider))
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
