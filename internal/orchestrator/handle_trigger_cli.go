package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runHandleTriggerCLI(args []string) error {
	fs := flag.NewFlagSet("handle-trigger", flag.ExitOnError)
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"),
		"controller URL (env: SPARKWING_CONTROLLER_URL)")
	logsURL := fs.String("logs", os.Getenv("SPARKWING_LOGS_URL"),
		"node log endpoint of the engine process that started this one (env: SPARKWING_LOGS_URL); the engine sets it")
	token := fs.String("token", agentToken,
		"bearer token for controller + logs calls (env: SPARKWING_AGENT_TOKEN)")
	heartbeat := fs.Duration("heartbeat", 0,
		"heartbeat cadence for the trigger claim (cluster mode only); 0 leaves it to the process that claimed the trigger, as the runner's trigger loop does")
	runnerKind := fs.String("runner", "inprocess", "node runner: inprocess | k8s | warm")
	k8sNamespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace for runner Jobs (k8s)")
	k8sImage := fs.String("image", "", "runner image (k8s)")
	k8sSA := fs.String("runner-sa", "", "service account name for runner pods (k8s)")
	k8sPullSecret := fs.String("image-pull-secret", "", "imagePullSecret for runner pods (k8s)")
	k8sCtrlURL := fs.String("runner-controller-url", "", "controller URL the runner pod should talk to (defaults to --controller)")
	artifactStoreURL := fs.String("artifact-store", os.Getenv("SPARKWING_CACHE_URL"), "artifact/cache store URL passed to runner pods (k8s)")
	dependencyProxy := fs.String("dependency-proxy", "",
		"base URL of the in-cluster pull-through package proxy stamped on runner pods as GOPROXY / npm_config_registry / PIP_INDEX_URL; "+
			"empty derives it from SPARKWING_GITCACHE_URL, \"off\" disables")
	imagePullPolicy := fs.String("image-pull-policy", "",
		"imagePullPolicy for runner pods: Always | IfNotPresent | Never (default IfNotPresent)")
	k8sCPUCeiling := fs.String("k8s-cpu-ceiling", "",
		"hard CPU ceiling for runner pods as a Kubernetes quantity (8, 500m); a pipeline pin or measured charge above it is clamped (empty = no ceiling)")
	k8sMemoryCeiling := fs.String("k8s-memory-ceiling", "",
		"hard memory ceiling for runner pods as a Kubernetes quantity (8Gi); a pipeline pin or measured charge above it is clamped (empty = no ceiling)")
	k8sJobDeadline := fs.String("k8s-job-deadline", "",
		"wall-clock bound on one runner Job as a Go duration (6h, 90m); Kubernetes kills a pod that outlives it, and a node's own .Timeout() outranks it (empty = 6h)")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster)")
	var k8sLabels stringSliceFlag
	fs.Var(&k8sLabels, "runner-label", "static capability every runner Job advertises (repeatable)")
	var k8sNodeSelector stringSliceFlag
	fs.Var(&k8sNodeSelector, "runner-node-selector", "node selector for runner pods, key=value (repeatable)")
	var k8sTolerations stringSliceFlag
	fs.Var(&k8sTolerations, "runner-toleration", "toleration for runner pods, key[=value]:Effect (repeatable)")
	k8sTeamNodes := fs.Bool("runner-team-nodes", false,
		"keep each band node to the team whose Job booted it by selecting sparkwing.dev/team-node; "+
			"the band pool must stamp that label on its nodes")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(positional) < 1 {
		return errors.New("usage: handle-trigger <trigger-id> --controller URL --token T")
	}
	triggerID := positional[0]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *controllerURL == "" {
		return errors.New("--controller (or SPARKWING_CONTROLLER_URL) required")
	}
	opts := WorkerOptions{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		Token:             *token,
		HeartbeatInterval: *heartbeat,
	}
	factory, err := BuildTriggerRunnerFactory(*runnerKind, K8sRunnerFactoryConfig{
		Kubeconfig:                 *kubeconfig,
		Namespace:                  *k8sNamespace,
		Image:                      *k8sImage,
		ServiceAccount:             *k8sSA,
		ImagePullSecret:            *k8sPullSecret,
		ControllerURL:              firstNonEmpty(*k8sCtrlURL, *controllerURL),
		DispatcherURL:              *controllerURL,
		ArtifactStoreURL:           *artifactStoreURL,
		GitcacheURL:                os.Getenv("SPARKWING_GITCACHE_URL"),
		AgentToken:                 *token,
		Labels:                     k8sLabels,
		TeamNodes:                  *k8sTeamNodes,
		DependencyProxyURL:         *dependencyProxy,
		DependencyProxyFallbackURL: os.Getenv("SPARKWING_GITCACHE_URL"),
		ImagePullPolicy:            *imagePullPolicy,
		CPUCeiling:                 *k8sCPUCeiling,
		MemoryCeiling:              *k8sMemoryCeiling,
		JobDeadline:                *k8sJobDeadline,
	}, k8sNodeSelector, k8sTolerations)
	if err != nil {
		return err
	}
	opts.RunnerFactory = factory
	if err := HandleClaimedTrigger(ctx, opts, triggerID); err != nil {
		return fmt.Errorf("handle %s: %w", triggerID, err)
	}
	return nil
}

// BuildTriggerRunnerFactory is the node runner kind names for each trigger:
// nil for inprocess, Kubernetes Jobs for k8s, and warm runners for warm,
// which fall back to Jobs when cfg names an image. nodeSelector and
// tolerations are the operator's key=value and key[=value]:Effect strings.
func BuildTriggerRunnerFactory(kind string, cfg K8sRunnerFactoryConfig, nodeSelector, tolerations []string,
) (func(Backends, *store.Trigger) runner.Runner, error) {
	k8s := func() (func(Backends, *store.Trigger) runner.Runner, error) {
		var err error
		if cfg.NodeSelector, err = parseK8sNodeSelector(nodeSelector); err != nil {
			return nil, err
		}
		if cfg.Tolerations, err = parseK8sTolerations(tolerations); err != nil {
			return nil, err
		}
		return BuildK8sRunnerFactory(cfg)
	}
	switch kind {
	case "", "inprocess":
		return nil, nil
	case "k8s":
		factory, err := k8s()
		if err != nil {
			return nil, fmt.Errorf("k8s runner: %w", err)
		}
		return factory, nil
	case "warm":
		var fallback func(Backends, *store.Trigger) runner.Runner
		if cfg.Image != "" {
			var err error
			if fallback, err = k8s(); err != nil {
				return nil, fmt.Errorf("warm runner (fallback k8s): %w", err)
			}
		}
		return BuildWarmRunnerFactory(cmp.Or(cfg.DispatcherURL, cfg.ControllerURL), cfg.AgentToken, warmpool.Config{}, fallback), nil
	}
	return nil, fmt.Errorf("--runner %q: expected inprocess, k8s, or warm", kind)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// HandleTriggerArgs is the handle-trigger argv a parent that claimed
// triggerID passes after the verb, flags first. A non-zero heartbeat makes
// the child renew the claim itself.
func HandleTriggerArgs(triggerID, controllerURL string, heartbeat time.Duration) []string {
	return []string{"--controller", controllerURL, "--heartbeat", heartbeat.String(), triggerID}
}

// safety: an older sparkwing cluster worker put the trigger ID before its
// flags, and the stdlib parser stops at the first positional, which dropped
// --heartbeat and left the claim to expire.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
