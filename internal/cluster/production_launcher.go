package cluster

import (
	"context"
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

const (
	launcherKubeQPS   = 50
	launcherKubeBurst = 100
)

func productionLauncher(version string) LauncherFactory {
	return func(ctx context.Context, jobs launcher.CloudJobConfig, bearer func(context.Context) (string, error)) (*launcher.Launcher, error) {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		if err := engine.RequirePolicyBuild("launcher", version, engine.DevelopmentBuildEnv+"=1", os.Getenv(engine.DevelopmentBuildEnv) == "1"); err != nil {
			return nil, err
		}
		config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load launcher Kubernetes configuration: %w", err)
		}
		// perf: in-cluster configuration leaves client-go at 5 requests/s per API group, and one
		// reconciliation pass drains that, so each placement call on the first-job handoff waited ~200 ms.
		config.QPS, config.Burst = launcherKubeQPS, launcherKubeBurst
		kube, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, err
		}
		karpenter, err := dynamic.NewForConfig(config)
		if err != nil {
			return nil, err
		}
		if err := jobs.Validate(); err != nil {
			return nil, err
		}
		boundary := &launcher.KubernetesBoundary{Kube: kube, Karpenter: karpenter, Namespace: jobs.Namespace, NodeClassName: jobs.NodeClassName}
		return launcher.NewLauncher(ctx, launcher.RuntimeConfig{Kube: kube, Boundary: boundary, Jobs: jobs, Bearer: bearer, Demand: controllerwire.ClaimRequest{Labels: jobs.Labels, RunnerVersion: version, Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}}})
	}
}
