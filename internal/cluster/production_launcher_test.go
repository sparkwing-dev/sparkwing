package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
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
	if _, err := productionLauncher("dev")(t.Context(), cfg, fixedBearer("private-token")); err == nil || !strings.Contains(err.Error(), engine.DevelopmentBuildEnv) {
		t.Fatalf("development launcher started without the override: %v", err)
	}
	runner, err := productionLauncher("v0.68.6-3-gabc1234")(t.Context(), cfg, fixedBearer("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(engine.DevelopmentBuildEnv, "1")
	if _, err := productionLauncher("dev")(t.Context(), cfg, fixedBearer("private-token")); err != nil {
		t.Fatalf("development override refused: %v", err)
	}
	if runner == nil || runner.Kube == nil || runner.Boundary == nil || runner.Control == nil {
		t.Fatal("launcher has no production services")
	}
	stop := errors.New("cancelled before Kubernetes")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(stop)
	if _, err = productionLauncher("dev")(ctx, cfg, fixedBearer("private-token")); !errors.Is(err, stop) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestProductionCloudJobRefusesDevelopmentBuild(t *testing.T) {
	t.Setenv(engine.DevelopmentBuildEnv, "1")
	_, _, err := productionRuntime("dev").Bootstrap.Host(t.Context(), BootstrapOptions{})
	if err == nil || !strings.Contains(err.Error(), `Cloud job build "dev" is a development build`) {
		t.Fatalf("development Cloud job started: %v", err)
	}
}
