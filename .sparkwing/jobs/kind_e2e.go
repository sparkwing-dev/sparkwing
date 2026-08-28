package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// KindE2E runs the release-shaped Kubernetes stack in a disposable Kind cluster.
type KindE2E struct{ sparkwing.Base }

func (KindE2E) ShortHelp() string {
	return "Prove the controller and runner golden path in a local Kind cluster"
}

func (KindE2E) Help() string {
	return "Builds the five deployable images from the checkout, loads them into a disposable Kind cluster, installs the full Helm chart, and exercises auth, GitHub webhook intake, runner execution, logs, restarts, retry, cancellation, and retained state. Requires a running local Docker daemon, kind, kubectl, helm, curl, jq, git, and openssl."
}

func (KindE2E) Examples() []sparkwing.Example {
	return []sparkwing.Example{{
		Comment: "Run the free local Kubernetes golden path",
		Command: "sparkwing run kind-e2e",
	}}
}

func (KindE2E) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "kind-e2e", runKindE2E).Timeout(40 * time.Minute)
	return nil
}

func runKindE2E(ctx context.Context) error {
	root, err := mainModuleRoot()
	if err != nil {
		return err
	}
	script := filepath.Join(root, "bin", "kind-e2e.sh")
	cmd := exec.CommandContext(ctx, "bash", script)
	configureKindE2ECommand(cmd)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Kind golden path: %w", err)
	}
	return nil
}

func init() {
	sparkwing.Register("kind-e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &KindE2E{} })
}
