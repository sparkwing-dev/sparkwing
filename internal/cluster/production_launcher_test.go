package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestProductionLauncherBindsKubernetesAndCloudBoundary(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: test\ncontexts:\n- name: test\n  context:\n    cluster: kube\nclusters:\n- name: kube\n  cluster:\n    server: https://kubernetes.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	cfg := launcher.CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: "https://controller.example", CPUCeiling: 1, MemoryCeiling: 1 << 30}
	runner, err := productionLauncher("dev")(t.Context(), cfg, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	if runner == nil || runner.Kube == nil || runner.Boundary == nil || runner.Control == nil {
		t.Fatal("launcher has no production services")
	}
	stop := errors.New("cancelled before Kubernetes")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(stop)
	if _, err = productionLauncher("dev")(ctx, cfg, "private-token"); !errors.Is(err, stop) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
