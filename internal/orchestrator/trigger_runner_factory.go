package orchestrator

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BuildTriggerRunnerFactory selects in-process or Kubernetes execution.
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

	}
	return nil, fmt.Errorf("--runner %q: expected inprocess or k8s", kind)
}
