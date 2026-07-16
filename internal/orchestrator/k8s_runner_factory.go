package orchestrator

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type K8sRunnerFactoryConfig struct {
	Kubeconfig       string
	Namespace        string
	Image            string
	ServiceAccount   string
	ImagePullSecret  string
	ControllerURL    string
	LogsURL          string
	ArtifactStoreURL string
	AgentToken       string
}

func BuildK8sRunnerFactory(cfg K8sRunnerFactoryConfig) (func(Backends, *store.Trigger) runner.Runner, error) {
	if cfg.Image == "" {
		return nil, fmt.Errorf("--image (or SPARKWING_RUNNER_IMAGE) is required with --runner k8s")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("--namespace (or POD_NAMESPACE) is required with --runner k8s")
	}
	if cfg.ControllerURL == "" {
		return nil, fmt.Errorf("runner must be given a controller URL")
	}
	var rc *rest.Config
	var err error
	if cfg.Kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
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
	runnerCfg := k8srunner.Config{
		Namespace:          cfg.Namespace,
		Image:              cfg.Image,
		ImagePullSecret:    cfg.ImagePullSecret,
		ServiceAccountName: cfg.ServiceAccount,
		ControllerURL:      cfg.ControllerURL,
		LogsURL:            cfg.LogsURL,
		ArtifactStoreURL:   cfg.ArtifactStoreURL,
		AgentToken:         cfg.AgentToken,
		CPURequest:         "100m",
		MemoryRequest:      "128Mi",
		CPULimit:           "2",
		MemoryLimit:        "2Gi",
		PollInterval:       time.Second,
	}
	logger := slog.Default()
	return func(_ Backends, _ *store.Trigger) runner.Runner {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		var ctrl *client.Client
		if cfg.AgentToken != "" {
			ctrl = client.NewWithToken(cfg.ControllerURL, httpClient, cfg.AgentToken)
		} else {
			ctrl = client.New(cfg.ControllerURL, httpClient)
		}
		return k8srunner.New(kcli, ctrl, runnerCfg, logger)
	}, nil
}
