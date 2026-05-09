package cluster

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runWorkerCLI implements `sparkwing-runner worker --controller URL [--poll DUR]`.
// Polls the controller for triggers and runs each pipeline in-
// process. Ctrl-C gracefully drains.
func runWorkerCLI(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	controllerURL := fs.String("controller", "", "controller base URL (required)")
	logsURL := fs.String("logs", "", "logs service URL (optional; local files if empty)")
	logStoreURL := fs.String("log-store", "",
		"pluggable log backend URL: fs:///abs/path or s3://bucket/prefix. "+
			"When set, takes precedence over --logs.")
	artifactStoreURL := fs.String("artifact-store", "",
		"pluggable artifact backend URL: fs:///abs/path or s3://bucket/prefix. "+
			"Validated at startup; consumed by future cache paths.")
	poll := fs.Duration("poll", time.Second, "poll interval when queue is empty")
	heartbeat := fs.Duration("heartbeat", 0, "heartbeat cadence (default: lease/3 = 10s)")
	runnerKind := fs.String("runner", "inprocess", "node runner: inprocess | k8s | warm")
	k8sNamespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace for runner Jobs (k8s | warm)")
	k8sImage := fs.String("image", os.Getenv("SPARKWING_RUNNER_IMAGE"), "runner image (k8s | warm fallback)")
	k8sSA := fs.String("runner-sa", os.Getenv("SPARKWING_RUNNER_SA"), "service account name for runner pods (k8s | warm fallback)")
	k8sPullSecret := fs.String("image-pull-secret", os.Getenv("SPARKWING_IMAGE_PULL_SECRET"), "imagePullSecret for runner pods (k8s | warm fallback)")
	k8sCtrlURL := fs.String("runner-controller-url", os.Getenv("SPARKWING_RUNNER_CONTROLLER_URL"), "controller URL the runner pod should talk to (defaults to --controller)")
	k8sLogsURL := fs.String("runner-logs-url", os.Getenv("SPARKWING_RUNNER_LOGS_URL"), "logs-service URL the runner pod should talk to (defaults to --logs)")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster)")
	warmWait := fs.Duration("warm-claim-wait", 5*time.Second,
		"how long the warm pool Runner waits for a pod to claim before falling back to K8sRunner")
	warmPoll := fs.Duration("warm-poll", 500*time.Millisecond,
		"how often the warm pool Runner polls GetNode while waiting")
	token := fs.String("token", os.Getenv("SPARKWING_AGENT_TOKEN"),
		"shared-secret bearer token for controller + logs auth (env: SPARKWING_AGENT_TOKEN)")
	metricsAddr := fs.String("metrics-addr", ":9090",
		"address for the /metrics listener (empty disables)")
	triggerSources := fs.String("trigger-sources", "",
		"comma-separated trigger_source values this worker handles (e.g. manual,schedule,pre_commit,pre_push); empty = accept any source")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *controllerURL == "" {
		fs.Usage()
		return fmt.Errorf("--controller is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-worker"})
	defer tel.Shutdown(context.Background())

	logger := slog.Default()
	go func() {
		if err := StartMetricsListener(ctx, *metricsAddr, logger); err != nil {
			logger.Error("metrics listener failed", "err", err)
		}
	}()

	opts := orchestrator.WorkerOptions{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		PollInterval:      *poll,
		HeartbeatInterval: *heartbeat,
		Delegate:          &stdoutLogger{},
		Token:             *token,
		Sources:           splitCSV(*triggerSources),
	}

	// storage flags. Resolved here so a malformed URL fails
	// fast at startup rather than the first node log.
	if *logStoreURL != "" {
		ls, err := storeurl.OpenLogStore(ctx, *logStoreURL)
		if err != nil {
			return fmt.Errorf("--log-store: %w", err)
		}
		opts.LogStore = ls
		logger.Info("log store", "url", *logStoreURL)
	}
	if *artifactStoreURL != "" {
		if _, err := storeurl.OpenArtifactStore(ctx, *artifactStoreURL); err != nil {
			return fmt.Errorf("--artifact-store: %w", err)
		}
		logger.Info("artifact store", "url", *artifactStoreURL,
			"note", "validated; not yet consumed by worker")
	}

	switch *runnerKind {
	case "", "inprocess":
		// Default: in-process. No factory needed.
	case "k8s":
		factory, err := buildK8sRunnerFactory(*kubeconfig, *k8sNamespace, *k8sImage,
			*k8sSA, *k8sPullSecret,
			firstNonEmpty(*k8sCtrlURL, *controllerURL),
			firstNonEmpty(*k8sLogsURL, *logsURL),
			*token)
		if err != nil {
			return fmt.Errorf("k8s runner: %w", err)
		}
		opts.RunnerFactory = factory
	case "warm":
		// Warm pool is primary, K8sRunner is fallback per session 3
		// plan. Building the K8s factory eagerly means a broken
		// kubeconfig fails startup rather than blowing up on the
		// first node that needs fallback.
		var k8sFactory func(orchestrator.Backends, *store.Trigger) runner.Runner
		if *k8sImage != "" {
			f, err := buildK8sRunnerFactory(*kubeconfig, *k8sNamespace, *k8sImage,
				*k8sSA, *k8sPullSecret,
				firstNonEmpty(*k8sCtrlURL, *controllerURL),
				firstNonEmpty(*k8sLogsURL, *logsURL),
				*token)
			if err != nil {
				return fmt.Errorf("warm runner (fallback k8s): %w", err)
			}
			k8sFactory = f
		}
		warmCfg := warmpool.Config{
			PollInterval:     *warmPoll,
			ClaimWaitTimeout: *warmWait,
		}
		opts.RunnerFactory = buildWarmPoolFactory(*controllerURL, *token, warmCfg, k8sFactory)
	default:
		return fmt.Errorf("--runner %q: expected inprocess, k8s, or warm", *runnerKind)
	}

	return RunWorker(ctx, opts)
}

// buildK8sRunnerFactory wires a kubernetes.Interface and closes over
// the shared K8sRunner config so every claimed trigger gets its own
// runner bound to the same cluster / namespace / image. The agentToken
// argument is stamped into each Job pod so the spawned runner's
// controller + logs calls authenticate under FOLLOWUPS #2 auth.
func buildK8sRunnerFactory(kubeconfig, namespace, image, sa, pullSecret, ctrlURL, logsURL, agentToken string) (func(orchestrator.Backends, *store.Trigger) runner.Runner, error) {
	if image == "" {
		return nil, fmt.Errorf("--image (or SPARKWING_RUNNER_IMAGE) is required with --runner k8s")
	}
	if namespace == "" {
		return nil, fmt.Errorf("--namespace (or POD_NAMESPACE) is required with --runner k8s")
	}
	if ctrlURL == "" {
		return nil, fmt.Errorf("runner must be given a controller URL (via --runner-controller-url or --controller)")
	}
	var rc *rest.Config
	var err error
	if kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	kcli, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("kube client: %w", err)
	}
	cfg := k8srunner.Config{
		Namespace:          namespace,
		Image:              image,
		ImagePullSecret:    pullSecret,
		ServiceAccountName: sa,
		ControllerURL:      ctrlURL,
		LogsURL:            logsURL,
		AgentToken:         agentToken,
		CPURequest:         "100m",
		MemoryRequest:      "128Mi",
		CPULimit:           "2",
		MemoryLimit:        "2Gi",
		PollInterval:       time.Second,
	}
	logger := slog.Default()
	return func(_ orchestrator.Backends, _ *store.Trigger) runner.Runner {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		var ctrl *client.Client
		if agentToken != "" {
			ctrl = client.NewWithToken(ctrlURL, httpClient, agentToken)
		} else {
			ctrl = client.New(ctrlURL, httpClient)
		}
		return k8srunner.New(kcli, ctrl, cfg, logger)
	}, nil
}

// buildWarmPoolFactory wraps a K8sRunner factory with the warm-pool
// Runner. Per trigger, each claim gets a fresh pool Runner (the K8s
// fallback inside it also closes over the trigger via the inner
// factory, so fallbacks pick up the right per-trigger identity if
// any field depends on it). Laptop use of --runner warm against a
// local controller works too: the K8s factory fails startup if it
// can't reach a cluster, which is the right signal.
func buildWarmPoolFactory(
	controllerURL, token string,
	cfg warmpool.Config,
	k8sFactory func(orchestrator.Backends, *store.Trigger) runner.Runner,
) func(orchestrator.Backends, *store.Trigger) runner.Runner {
	return func(b orchestrator.Backends, t *store.Trigger) runner.Runner {
		ctrl := client.NewWithToken(controllerURL, &http.Client{Timeout: 30 * time.Second}, token)
		var fallback runner.Runner
		if k8sFactory != nil {
			fallback = k8sFactory(b, t)
		}
		return warmpool.New(ctrl, fallback, cfg, slog.Default())
	}
}
