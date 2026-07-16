package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"
)

// runHandleTriggerCLI handles `sparkwing handle-trigger <id> [flags]`.
// Adopts an already-claimed trigger and runs it to terminal state.
// --local skips the controller and uses LocalBackends.
func runHandleTriggerCLI(args []string) error {
	fs := flag.NewFlagSet("handle-trigger", flag.ExitOnError)
	controllerURL := fs.String("controller", ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"),
		"controller URL (env: SPARKWING_CONTROLLER_URL, falls back to $SPARKWING_HOME/dev.env)")
	logsURL := fs.String("logs", ResolveDevEnvURL("SPARKWING_LOGS_URL"),
		"logs service URL (env: SPARKWING_LOGS_URL, falls back to $SPARKWING_HOME/dev.env)")
	token := fs.String("token", os.Getenv("SPARKWING_AGENT_TOKEN"),
		"bearer token for controller + logs calls (env: SPARKWING_AGENT_TOKEN)")
	heartbeat := fs.Duration("heartbeat", 5*time.Second,
		"heartbeat cadence for the claim lease (cluster mode only)")
	runnerKind := fs.String("runner", "inprocess", "node runner: inprocess | k8s")
	k8sNamespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace for runner Jobs (k8s)")
	k8sImage := fs.String("image", os.Getenv("SPARKWING_RUNNER_IMAGE"), "runner image (k8s)")
	k8sSA := fs.String("runner-sa", os.Getenv("SPARKWING_RUNNER_SA"), "service account name for runner pods (k8s)")
	k8sPullSecret := fs.String("image-pull-secret", os.Getenv("SPARKWING_IMAGE_PULL_SECRET"), "imagePullSecret for runner pods (k8s)")
	k8sCtrlURL := fs.String("runner-controller-url", os.Getenv("SPARKWING_RUNNER_CONTROLLER_URL"), "controller URL the runner pod should talk to (defaults to --controller)")
	k8sLogsURL := fs.String("runner-logs-url", os.Getenv("SPARKWING_RUNNER_LOGS_URL"), "logs-service URL the runner pod should talk to (defaults to --logs)")
	artifactStoreURL := fs.String("artifact-store", os.Getenv("SPARKWING_CACHE_URL"), "artifact/cache store URL passed to runner pods (k8s)")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster)")
	local := fs.Bool("local", false,
		"run against the laptop SQLite store; no controller required")
	profileName := fs.String("profile", "",
		"profile to resolve backends from (local mode only). Forwarded by "+
			"the parent's local trigger dispatcher so the child opens the "+
			"same state backend the parent enqueued the trigger in.")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		return errors.New("usage: handle-trigger <trigger-id> [--controller URL --token T | --local [--profile NAME]]")
	}
	triggerID := fs.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *local {
		if err := HandleClaimedTriggerLocal(ctx, triggerID, *profileName); err != nil {
			return fmt.Errorf("handle %s (local): %w", triggerID, err)
		}
		return nil
	}

	if *controllerURL == "" {
		return errors.New("--controller (or SPARKWING_CONTROLLER_URL) required (or pass --local)")
	}
	opts := WorkerOptions{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		Token:             *token,
		HeartbeatInterval: *heartbeat,
	}
	switch *runnerKind {
	case "", "inprocess":
	case "k8s":
		factory, err := BuildK8sRunnerFactory(K8sRunnerFactoryConfig{
			Kubeconfig:       *kubeconfig,
			Namespace:        *k8sNamespace,
			Image:            *k8sImage,
			ServiceAccount:   *k8sSA,
			ImagePullSecret:  *k8sPullSecret,
			ControllerURL:    firstNonEmpty(*k8sCtrlURL, *controllerURL),
			LogsURL:          firstNonEmpty(*k8sLogsURL, *logsURL),
			ArtifactStoreURL: *artifactStoreURL,
			AgentToken:       *token,
		})
		if err != nil {
			return fmt.Errorf("k8s runner: %w", err)
		}
		opts.RunnerFactory = factory
	default:
		return fmt.Errorf("--runner %q: expected inprocess or k8s", *runnerKind)
	}
	if err := HandleClaimedTrigger(ctx, opts, triggerID); err != nil {
		return fmt.Errorf("handle %s: %w", triggerID, err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
