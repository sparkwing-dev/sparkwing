package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// KubernetesE2E verifies the release-shaped stack in an explicit Kubernetes cluster.
type KubernetesE2E struct{ sparkwing.Base }

func (KubernetesE2E) ShortHelp() string {
	return "Prove the controller and runner golden path in Kubernetes"
}

func (KubernetesE2E) Help() string {
	return "Installs the full Helm chart in an explicit Kubernetes context and exercises auth, GitHub webhook intake, runner execution, logs, restarts, retry, cancellation, and retained state. It requires caller-supplied image coordinates and an exact namespace/release cleanup allow-list; it never creates or deletes cluster infrastructure. Native Windows cancellation force-terminates the full process tree; use WSL when cancellation must run the shell cleanup trap."
}

func (KubernetesE2E) Examples() []sparkwing.Example {
	return []sparkwing.Example{{
		Comment: "Run the Kubernetes golden path against an explicit cluster",
		Command: "sparkwing run k8s-e2e",
	}}
}

func (KubernetesE2E) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "k8s-e2e", runKubernetesE2E).Timeout(40 * time.Minute)
	return nil
}

func runKubernetesE2E(ctx context.Context) error {
	root, err := mainModuleRoot()
	if err != nil {
		return err
	}
	script := filepath.Join(root, "bin", "k8s-e2e.sh")
	if err := runKubernetesE2EScript(ctx, root, script); err != nil {
		return fmt.Errorf("kubernetes golden path: %w", err)
	}
	return nil
}

func runKubernetesE2EScript(ctx context.Context, root, script string) error {
	if runtime.GOOS == "windows" {
		_, err := sparkwing.Exec(ctx, "bash", filepath.ToSlash(script)).Dir(root).Run()
		return err
	}
	cmd := exec.CommandContext(ctx, "bash", script)
	configureKubernetesE2ECommand(cmd)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func init() {
	sparkwing.Register("k8s-e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &KubernetesE2E{} })
}
