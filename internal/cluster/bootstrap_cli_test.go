package cluster

import (
	"context"
	"errors"
	"testing"
)

func TestBootstrapCommandsPassImmutableAwardAndPrivatePaths(t *testing.T) {
	want := BootstrapOptions{ControllerURL: "https://controller.example", RunID: "run", JobID: "job", Generation: 7, BootstrapFile: "/private/bootstrap", SourceDir: "/scratch/source", ModuleCache: "/scratch/modules"}
	for _, command := range []string{"run-node", "fetch-source"} {
		t.Run(command, func(t *testing.T) {
			boundary := errors.New("host boundary")
			err := RunBootstrapCLI(t.Context(), command, []string{"--controller=" + want.ControllerURL, "--run=run", "--job=job", "--generation=7", "--bootstrap-file=" + want.BootstrapFile, "--source-dir=" + want.SourceDir, "--module-cache=" + want.ModuleCache}, func(ctx context.Context, got BootstrapOptions) error {
				if ctx != t.Context() || got != want {
					t.Errorf("bootstrap: %#v", got)
				}
				return boundary
			})
			if !errors.Is(err, boundary) {
				t.Fatal(err)
			}
		})
	}
}

func TestBootstrapCommandsRefuseUnfencedOrEnvironmentAuthority(t *testing.T) {
	t.Setenv("SPARKWING_CONTROLLER_URL", "https://old.example")
	t.Setenv("SPARKWING_AGENT_TOKEN", "old-token")
	for _, command := range []string{"run-node", "fetch-source"} {
		for _, args := range [][]string{nil, {"run", "job"}, {"--token=secret"}, {"--node=job"}, {"--claim-generation=7"}} {
			if err := RunBootstrapCLI(t.Context(), command, args, func(context.Context, BootstrapOptions) error { t.Error("unfenced request reached host"); return nil }); err == nil {
				t.Errorf("%s accepted %v", command, args)
			}
		}
	}
}
