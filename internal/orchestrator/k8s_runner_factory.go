package orchestrator

import (
	"cmp"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type K8sRunnerFactoryConfig struct {
	Kubeconfig      string
	Namespace       string
	Image           string
	ServiceAccount  string
	ImagePullSecret string
	// ControllerURL is the controller a Job's pod talks to.
	ControllerURL string
	// DispatcherURL is the controller the dispatcher's own calls go to, with
	// AgentToken; empty uses ControllerURL. A trigger child behind its
	// runner's broker reaches the controller at a loopback address no pod
	// can.
	DispatcherURL string
	// AgentToken is the dispatcher's own credential for its controller
	// calls. A Job never receives it; each Job carries its node's claim
	// token instead.
	AgentToken   string
	Labels       []string
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	TeamNodes    bool

	ImagePullPolicy string

	CPUCeiling string

	MemoryCeiling string

	// JobDeadline is the operator's wall-clock bound on one runner Job as a Go
	// duration. Empty leaves the runner's own default.
	JobDeadline string
}

func BuildK8sRunnerFactory(cfg K8sRunnerFactoryConfig) (func(Backends, *store.Trigger) runner.Runner, error) {
	if cfg.Image == "" {
		return nil, fmt.Errorf("--image is required with --runner k8s")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("--namespace (or POD_NAMESPACE) is required with --runner k8s")
	}
	if cfg.ControllerURL == "" {
		return nil, fmt.Errorf("runner must be given a controller URL")
	}
	// safety: an empty name silently lands runner pods on the namespace default SA
	if cfg.ServiceAccount == "" {
		return nil, fmt.Errorf("--runner-sa is required with --runner k8s")
	}
	pullPolicy, err := k8srunner.ParsePullPolicy(cfg.ImagePullPolicy)
	if err != nil {
		return nil, fmt.Errorf("--image-pull-policy: %w", err)
	}
	// safety: a ceiling that fails to parse must refuse to start, not silently cap nothing
	cpuCeiling, err := k8srunner.ParseCPUCeiling(cfg.CPUCeiling)
	if err != nil {
		return nil, fmt.Errorf("--k8s-cpu-ceiling: %w", err)
	}
	memoryCeiling, err := k8srunner.ParseMemoryCeiling(cfg.MemoryCeiling)
	if err != nil {
		return nil, fmt.Errorf("--k8s-memory-ceiling: %w", err)
	}
	jobDeadline, err := k8srunner.ParseJobDeadline(cfg.JobDeadline)
	if err != nil {
		return nil, fmt.Errorf("--k8s-job-deadline: %w", err)
	}
	var rc *rest.Config
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
		ImagePullPolicy:    pullPolicy,
		Labels:             cfg.Labels,
		NodeSelector:       cfg.NodeSelector,
		TeamNodes:          cfg.TeamNodes,
		Tolerations:        cfg.Tolerations,
		CPURequest:         "100m",
		MemoryRequest:      "128Mi",
		CPULimit:           "2",
		MemoryLimit:        "2Gi",
		CPUCeiling:         cpuCeiling,
		MemoryCeiling:      memoryCeiling,
		JobActiveDeadline:  jobDeadline,
		PollInterval:       time.Second,
	}
	logger := slog.Default()
	return func(_ Backends, trigger *store.Trigger) runner.Runner {
		runCfg := runnerCfg
		if trigger != nil {
			runCfg.Team = string(trigger.Team)
		}
		httpClient := &http.Client{Timeout: 30 * time.Second}
		var ctrl *client.Client
		dispatcherURL := cmp.Or(cfg.DispatcherURL, cfg.ControllerURL)
		if cfg.AgentToken != "" {
			ctrl = client.NewWithToken(dispatcherURL, httpClient, cfg.AgentToken)
		} else {
			ctrl = client.New(dispatcherURL, httpClient)
		}
		return k8srunner.New(kcli, ctrl, runCfg, logger)
	}, nil
}
