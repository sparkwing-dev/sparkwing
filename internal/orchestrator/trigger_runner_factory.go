package orchestrator

import (
	"cmp"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
