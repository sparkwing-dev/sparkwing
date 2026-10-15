package acceptance_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestRunnerCredentialW8Components(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	directory, err := fssecure.MkdirPrivateTemp(filepath.Dir(path), "credentials-")
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(directory, "agent-token")
	const raw = "private-server-issued-runner-credential"
	if err := fssecure.WriteFile(credential, []byte(raw+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.WriteFile(path, []byte("runner:\n  controller: https://controller.example\n  credentials_dir: "+directory+"\n  repos: [github.com/acme/*]\n")); err != nil {
		t.Fatal(err)
	}
	reached := errors.New("runner composition")
	cli := cluster.RunnerCLI{Factory: func(ctx context.Context, options cluster.RunnerOptions) (runners.RuntimeConfig, error) {
		if options.Bearer == nil || len(options.Config.Repos) != 1 || options.Config.Repos[0] != "github.com/acme/*" {
			t.Fatal("startup did not retain enrollment credentials and repository fence")
		}
		if err := os.Remove(credential); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := options.Bearer(ctx)
			if err != nil || got != raw {
				t.Fatalf("startup did not read the private credential once: %v", err)
			}
		}
		cancelled, cancel := context.WithCancelCause(ctx)
		cause := errors.New("stopped transport")
		cancel(cause)
		if got, err := options.Bearer(cancelled); got != "" || !errors.Is(err, cause) {
			t.Fatalf("cancelled transport received a credential: %v", err)
		}
		return runners.RuntimeConfig{}, reached
	}}
	if err := cli.Run(t.Context(), "start", []string{"--config", path}, "v1.2.3"); !errors.Is(err, reached) {
		t.Fatal(err)
	}
	cli.Factory = func(context.Context, cluster.RunnerOptions) (runners.RuntimeConfig, error) {
		t.Fatal("missing private credential reached composition")
		return runners.RuntimeConfig{}, nil
	}
	if err := cli.Run(t.Context(), "start", []string{"--config", path}, "v1.2.3"); err == nil || strings.Contains(err.Error(), raw) {
		t.Fatalf("missing credential was accepted or exposed: %v", err)
	}
}
