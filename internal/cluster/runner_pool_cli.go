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

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type PoolLoopConfig struct {
	ControllerURL     string
	LogsURL           string
	GitcacheURL       string
	// AllowRepos binds every claimed node the way TriggerLoopOptions.AllowRepos
	// binds a trigger.
	AllowRepos        sourceurl.RepoAllowlist
	Token             string
	HolderPrefix      string
	Labels            []string
	MaxConcurrent     int
	SharedSlots       chan struct{}
	PollInterval      time.Duration
	Lease             time.Duration
	HeartbeatInterval time.Duration

	MaxClaims int

	// IdleExit ends the loop once no node has been held for this long, so a
	// runner on metered minutes stops paying for an empty queue. Zero polls
	// until cancelled.
	IdleExit time.Duration
	// ClaimUntil stops new claims at this instant and ends the loop once the
	// nodes already held finish. Zero never stops.
	ClaimUntil time.Time

	SourceName string

	LocalAdmission bool

	LocalReserve string

	Contribution           string
	MembershipContribution string

	Home string

	Version string
}

type nodeClaimer interface {
	ClaimNodeWithCapacity(ctx context.Context, holderID string, labels []string, lease time.Duration,
		headroom *client.Headroom, capacity *client.ClaimCapacity) (*store.Node, error)
}

type poolExecFn func(ctx context.Context, n *store.Node, holderID string)

func RunPoolLoop(ctx context.Context, cfg PoolLoopConfig, logger *slog.Logger) error {
	if cfg.ControllerURL == "" {
		return errors.New("pool loop: ControllerURL is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = normalizePoolLoopConfig(cfg)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctrl := client.NewWithToken(cfg.ControllerURL, httpClient, cfg.Token).
		WithRunnerIdentity(holderRunnerIdentity(cfg.HolderPrefix))
	if cfg.GitcacheURL == "" || !cfg.AllowRepos.Empty() {
		ctrl.WithAllowRepos(cfg.AllowRepos.Patterns())
	}

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
		contribution, err := parseReserve(cfg.Contribution)
		if err != nil {
			return fmt.Errorf("pool loop: contribution: %w", err)
		}
		membershipContribution, err := parseReserve(cfg.MembershipContribution)
		if err != nil {
			return fmt.Errorf("pool loop: membership contribution: %w", err)
		}
		provider = newHeadroomProvider(cfg.Home, cfg.Version, rv, contribution, membershipContribution)
		logger.Info("local admission engaged; controller work shares the local daemon",
			"reserve", cfg.LocalReserve, "source", cfg.SourceName)
	}

	exec := func(execCtx context.Context, n *store.Node, holderID string) {
		executePooledNode(execCtx, ctrl, cfg.ControllerURL, cfg.LogsURL, cfg.GitcacheURL, cfg.AllowRepos, cfg.Token,
			n, holderID, cfg.Lease, cfg.HeartbeatInterval, cfg.SourceName, logger, admission, provider)
	}
	return runPoolLoop(ctx, cfg, ctrl, exec, provider, logger)
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
	return cfg
}

func runPoolLoop(ctx context.Context, cfg PoolLoopConfig, claimer nodeClaimer, exec poolExecFn, provider headroomProvider, logger *slog.Logger) error {
	logger.Info(
		cfg.SourceName+" started",
		"controller", cfg.ControllerURL,
		"logs", cfg.LogsURL,
		"max_concurrent", cfg.MaxConcurrent,
		"max_claims", cfg.MaxClaims,
		"poll", cfg.PollInterval,
		"holder_prefix", cfg.HolderPrefix,
		"labels", cfg.Labels,
		"auth", cfg.Token != "",
		"idle_exit", cfg.IdleExit,
	)

	advisor, _ := claimer.(client.PollAdvisor)
	idlePoll := func() time.Duration { return client.AdvisedPoll(cfg.PollInterval, advisor) }

	sem := make(chan struct{}, cfg.MaxConcurrent)
	sharedSlots := cfg.SharedSlots
	var wg sync.WaitGroup
	defer wg.Wait()

	claimed := 0
	// safety: a spent credit balance persists across every poll, so the log
	// says so once rather than twice a second until it is topped up.
	creditsLogged := false
	// safety: a compute guard holds for as long as the work above it runs, so
	// the log says so once rather than on every poll.
	limitLogged := false
	shed := client.NewShedLog(client.ShedWarnInterval)
	idleSince := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			logger.Info(cfg.SourceName+" shutting down", "reason", err)
			return nil
		}
		if cfg.MaxClaims > 0 && claimed >= cfg.MaxClaims {
			logger.Info(cfg.SourceName+" max-claims reached; exiting for container restart",
				"claimed", claimed, "max_claims", cfg.MaxClaims)
			return nil
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		if sharedSlots != nil {
			select {
			case sharedSlots <- struct{}{}:
			case <-ctx.Done():
				<-sem
				return nil
			}
		}

		// safety: checked after the slot wait, because a node that held the
		// only slot may have run past the deadline.
		if !cfg.ClaimUntil.IsZero() && !time.Now().Before(cfg.ClaimUntil) {
			<-sem
			if sharedSlots != nil {
				<-sharedSlots
			}
			logger.Info(cfg.SourceName+" credential is near expiry; finishing held nodes and exiting",
				"claimed", claimed)
			return nil
		}
		holderID := fmt.Sprintf("%s:%d", cfg.HolderPrefix, time.Now().UnixNano())
		report := currentCapacity(ctx, provider)
		// safety: the loop holds one slot for the claim it is about to make, so
		// the nodes already executing are the rest of what it holds.
		capacity := &client.ClaimCapacity{MaxConcurrent: cfg.MaxConcurrent, ActiveClaims: max(len(sem)-1, 0)}
		n, err := claimer.ClaimNodeWithCapacity(ctx, holderID, cfg.Labels, cfg.Lease, report.headroom, capacity)
		if err != nil {
			<-sem
			if sharedSlots != nil {
				<-sharedSlots
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if errors.Is(err, store.ErrComputeLimit) {
				observeClaimOutcome("compute-limit")
				if !limitLogged {
					limitLogged = true
					logger.Error("claim withheld; a compute guard is holding this runner back",
						"err", err, "source", cfg.SourceName)
				}
				sleepOrCancel(ctx, cfg.PollInterval)
				continue
			}
			if errors.Is(err, store.ErrInsufficientCredits) {
				observeClaimOutcome("insufficient-credits")
				if !creditsLogged {
					creditsLogged = true
					logger.Error("claim withheld; the controller's credit balance is spent",
						"err", err, "source", cfg.SourceName)
				}
				sleepOrCancel(ctx, cfg.PollInterval)
				continue
			}
			if wait, ok := client.UnavailableBackoff(err, cfg.PollInterval); ok {
				observeClaimOutcome("unavailable")
				logger.Debug("claim shed by the controller; backing off",
					"err", err, "retry_after", wait, "source", cfg.SourceName)
				if shed.Due() {
					logger.Warn("controller is shedding claims; polling more slowly",
						"err", err, "retry_after", wait, "source", cfg.SourceName)
				}
				sleepOrCancel(ctx, wait)
				continue
			}
			observeClaimOutcome("error")
			logger.Error("claim failed", "err", err, "source", cfg.SourceName)
			sleepOrCancel(ctx, cfg.PollInterval)
			continue
		}
		if n == nil {
			<-sem
			if sharedSlots != nil {
				<-sharedSlots
			}
			observeClaimOutcome("empty")
			// safety: a slot still held means a node is running, and the
			// runner stays until it finishes however long the queue is empty.
			if len(sem) > 0 {
				idleSince = time.Now()
			} else if cfg.IdleExit > 0 && time.Since(idleSince) >= cfg.IdleExit {
				logger.Info(cfg.SourceName+" queue empty; exiting",
					"idle", time.Since(idleSince).Round(time.Second), "claimed", claimed)
				return nil
			}
			sleepOrCancel(ctx, idlePoll())
			continue
		}
		observeClaimOutcome("claimed")
		idleSince = time.Now()
		creditsLogged = false
		limitLogged = false
		claimed++

		logger.Info("claimed node",
			"run_id", n.RunID, "node_id", n.NodeID,
			"holder", holderID, "source", cfg.SourceName)

		wg.Add(1)
		go func(n *store.Node, holderID string) {
			defer wg.Done()
			defer func() { <-sem }()
			if sharedSlots != nil {
				defer func() { <-sharedSlots }()
			}
			exec(ctx, n, holderID)
		}(n, holderID)
	}
}

func executorKind(source string) string {
	if source == "agent" {
		return "agent"
	}
	return "runner"
}

func runRunnerCLI(args []string, version string) error {
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
		"the operator's git cache, which triggers and nodes fetch source through; empty fetches each run's "+
			"repository directly with this machine's own git credentials (env: SPARKWING_GITCACHE_URL)")
	var allowRepos multiFlag
	fs.Var(&allowRepos, "allow-repo",
		"repository this machine may build, as host/path with '*' matching within one path segment "+
			"(repeatable, e.g. --allow-repo 'github.com/acme/*'); required without --gitcache, since the runner "+
			"then fetches, compiles and runs each run's pipeline code as the user running it")
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
	var triggerRunnerLabels multiFlag
	fs.Var(&triggerRunnerLabels, "trigger-runner-label",
		"static capability every trigger-spawned runner Job advertises (repeatable)")
	var triggerRunnerNodeSelector multiFlag = splitCSV(os.Getenv("SPARKWING_RUNNER_NODE_SELECTOR"))
	fs.Var(&triggerRunnerNodeSelector, "trigger-runner-node-selector",
		"node selector for trigger-spawned runner Jobs, key=value (repeatable; env: SPARKWING_RUNNER_NODE_SELECTOR)")
	var triggerRunnerTolerations multiFlag = splitCSV(os.Getenv("SPARKWING_RUNNER_TOLERATION"))
	fs.Var(&triggerRunnerTolerations, "trigger-runner-toleration",
		"toleration for trigger-spawned runner Jobs, key[=value]:Effect (repeatable; env: SPARKWING_RUNNER_TOLERATION)")
	warmModules := fs.String("warm-modules", os.Getenv("SPARKWING_WARM_MODULES"),
		"comma-separated modules downloaded into GOMODCACHE at startup so the first pipeline compile after a "+
			"restart is not fully cold; each entry may carry an @version, \"off\" warms nothing "+
			"(default: the Sparkwing SDK at this runner's version; env: SPARKWING_WARM_MODULES)")
	localAdmission := fs.Bool("local-admission", false,
		"route claimed nodes through this box's local admission daemon (for a runner on a box that also runs local pipelines; off for in-cluster pods)")
	localReserve := fs.String("local-reserve", os.Getenv("SPARKWING_LOCAL_RESERVE"),
		"host capacity held back from advertised headroom in the daemon budget grammar, e.g. 2,4gb or 10% (env: SPARKWING_LOCAL_RESERVE)")
	githubActions := fs.Bool("github-actions", false,
		"run inside a GitHub Actions job: exchange the job's ID token for a credential that claims only this repository's work, "+
			"advertise the github-actions label, and stop claiming before the credential expires")
	team := fs.String("team", os.Getenv("SPARKWING_TEAM"),
		"team slug whose work this GitHub Actions job runs; the team's owner must have bound this repository (env: SPARKWING_TEAM)")
	idleExit := fs.Duration("idle-exit", 0,
		"exit once no node has been held for this long (0 = poll until stopped)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if *controllerURL == "" {
		fs.Usage()
		return errors.New("--controller is required")
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
	if !*claimNodes && !*alsoClaimTriggers {
		return errors.New("--claim-nodes=false requires --also-claim-triggers")
	}
	if *idleExit < 0 {
		return errors.New("--idle-exit must not be negative")
	}
	allow, err := sourceurl.ParseRepoAllowlist(allowRepos)
	if err != nil {
		return fmt.Errorf("--allow-repo: %w", err)
	}
	var claimUntil time.Time
	if *githubActions {
		if *alsoClaimTriggers || !*claimNodes {
			return errors.New("--github-actions claims this repository's nodes; drop --also-claim-triggers and --claim-nodes=false")
		}
		cred, err := githubActionsCredential(context.Background(), &http.Client{Timeout: githubExchangeTimeout},
			*controllerURL, *team)
		if err != nil {
			return fmt.Errorf("--github-actions: %w", err)
		}
		*token = cred.Token
		for _, l := range cred.Labels {
			labels = withLabel(labels, l)
		}
		claimUntil = githubClaimDeadline(cred)
		if !explicit["holder-prefix"] {
			*holderPrefix = "github-actions:" + cred.Repository + ":" + os.Getenv("GITHUB_RUN_ID")
		}
		// safety: the job itself is the unit that restarts, so the pod-restart
		// claim budget would only end a job early.
		if !explicit["max-claims-before-restart"] {
			*maxClaims = 0
		}
		if !explicit["metrics-addr"] {
			*metricsAddr = ""
		}
		// safety: the credential claims only this repository's work, so a job
		// given no list builds exactly the repository that started it.
		if allow.Empty() {
			if allow, err = sourceurl.ParseRepoAllowlist([]string{"github.com/" + cred.Repository}); err != nil {
				return fmt.Errorf("--github-actions: %w", err)
			}
		}
		slog.Default().Info("github actions runner credential issued",
			"team", cred.Team, "repository", cred.Repository,
			"expires_at", time.Unix(cred.ExpiresAt, 0).UTC(), "claim_until", claimUntil.UTC())
	}

	if *gitcacheURL == "" && allow.Empty() {
		return errors.New("--allow-repo is required without --gitcache: this runner fetches, compiles and runs " +
			"pipeline code as the user running it, from whatever repository a run names, so name the repositories " +
			"you trust, e.g. --allow-repo 'github.com/acme/*'")
	}
	slog.Default().Info("runner repository allowlist", "allow_repo", allow.String(), "direct_source", *gitcacheURL == "")

	identity := buildinfo.Read("sparkwing-runner", version)
	warmList, err := parseWarmModules(*warmModules, identity.Version)
	if err != nil {
		return fmt.Errorf("--warm-modules: %w", err)
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
				AllowRepos:      allow,
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
				K8sLabels:       triggerRunnerLabels,
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
	// safety: warming in the foreground would hold every claim behind a download.
	go warmModuleCache(ctx, warmList, logger)

	if !*claimNodes {
		<-ctx.Done()
		return nil
	}

	return RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		GitcacheURL:       *gitcacheURL,
		AllowRepos:        allow,
		Token:             *token,
		HolderPrefix:      *holderPrefix,
		Labels:            []string(labels),
		MaxConcurrent:     *maxConcurrent,
		PollInterval:      *poll,
		Lease:             *lease,
		HeartbeatInterval: *heartbeat,
		MaxClaims:         *maxClaims,
		IdleExit:          *idleExit,
		ClaimUntil:        claimUntil,
		SourceName:        "pool runner",
		LocalAdmission:    *localAdmission,
		LocalReserve:      *localReserve,
	}, slog.Default())
}

func currentCapacity(ctx context.Context, provider headroomProvider) capacityReport {
	if provider == nil {
		return capacityReport{}
	}
	return provider(ctx)
}

func executePooledNode(
	ctx context.Context,
	ctrl *client.Client,
	controllerURL, logsURL, gitcacheURL string,
	allow sourceurl.RepoAllowlist,
	token string,
	n *store.Node,
	holderID string,
	lease, hbInterval time.Duration,
	source string,
	logger *slog.Logger,
	admission *orchestrator.LocalAdmission,
	provider headroomProvider,
) {
	if hbInterval <= 0 {
		hbInterval = store.PoolHeartbeatInterval
	}
	if hbInterval < 200*time.Millisecond {
		hbInterval = 200 * time.Millisecond
	}

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatCtx := store.WithNodeClaimFence(execCtx, store.NodeClaimFence{
		HolderID: holderID, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		runPoolHeartbeat(heartbeatCtx, ctrl, n.RunID, n.NodeID, holderID, lease, hbInterval, cancel, source, provider, logger)
	}()

	grant := requestRunCacheGrant(execCtx, controllerURL, token, n.RunID, logger)
	res, err := runPooledNodeOnce(execCtx, controllerURL, logsURL, n.RunID, n.NodeID, holderID, token,
		&stdoutLogger{}, logger, admission, orchestrator.WithGitcache(gitcacheURL, grant), orchestrator.WithRepoAllowlist(allow),
		orchestrator.ClaimedNodeAttempt(n))
	cancel()
	hbWG.Wait()

	if err != nil {
		logger.Error(source+" setup failure",
			"run_id", n.RunID, "node_id", n.NodeID, "err", err)
		failPooledNodeSetup(ctx, ctrl, n, holderID, err, source, logger)
		return
	}
	logger.Info(source+" finished node",
		"run_id", n.RunID, "node_id", n.NodeID, "outcome", res.Outcome)
}

// runPooledNodeOnce is the node execution a pooled claim runs; tests
// replace it to inject a setup failure.
var runPooledNodeOnce = orchestrator.RunNodeOnce

// poolSetupFinishTimeout bounds the finish a setup failure sends.
const poolSetupFinishTimeout = 10 * time.Second

// failPooledNodeSetup finishes a claimed node as failed with the setup
// error, under the node's claim fence, so the run reports the real cause at
// once instead of waiting out the claim lease. A runner shutting down leaves
// the node alone, because its lease lapsing is what hands the node to
// another runner.
func failPooledNodeSetup(ctx context.Context, ctrl *client.Client, n *store.Node, holderID string, setupErr error, source string, logger *slog.Logger) {
	if ctx.Err() != nil {
		return
	}
	finishCtx, cancel := context.WithTimeout(store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: holderID, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}), poolSetupFinishTimeout)
	defer cancel()
	msg := source + " could not start the node: " + setupErr.Error()
	if err := ctrl.FinishNodeWithReason(finishCtx, n.RunID, n.NodeID, string(sparkwing.Failed), msg, nil, store.FailureUnknown, nil); err != nil {
		logger.Warn(source+" could not finish the node after its setup failed; its lease will lapse",
			"run_id", n.RunID, "node_id", n.NodeID, "err", err)
	}
}

// requestRunCacheGrant returns the grant a claimed run's cache traffic carries,
// or "" when the controller mints none; the run then goes without the binary
// and dependency caches rather than failing.
func requestRunCacheGrant(ctx context.Context, controllerURL, token, runID string, logger *slog.Logger) string {
	grant, err := bincache.RequestCacheGrant(ctx, controllerURL, token, runID)
	switch {
	case err == nil:
		return grant
	case errors.Is(err, bincache.ErrNoCacheGrant):
		logger.Debug("controller mints no cache grant; running without the cache", "run_id", runID)
	default:
		logger.Warn("cache grant unavailable; running without the cache", "run_id", runID, "err", err)
	}
	return ""
}

var (
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
	shed := client.NewShedLog(client.ShedWarnInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(ctx, poolHeartbeatTimeout)
			report := currentCapacity(hbCtx, provider)
			err := ctrl.HeartbeatNodeClaim(hbCtx, runID, nodeID, holderID, lease, report.headroom)
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
			if wait, ok := client.UnavailableBackoff(err, minShedBackoff); ok {
				logger.Debug(source+" heartbeat shed by the controller; backing off",
					"run_id", runID, "node_id", nodeID,
					"retry_after", wait, "err", err)
				if shed.Due() {
					logger.Warn(source+" heartbeat: controller is shedding heartbeats",
						"run_id", runID, "node_id", nodeID,
						"retry_after", wait, "err", err,
						"silence", silence.Round(time.Second))
				}
				sleepOrCancel(ctx, wait)
				continue
			}
			logger.Warn(source+" heartbeat failed",
				"run_id", runID, "node_id", nodeID,
				"err", err,
				"silence", silence.Round(time.Second))
		}
	}
}
