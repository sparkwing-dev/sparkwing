package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestBootstrapCommandsPassImmutableAwardAndPrivatePaths(t *testing.T) {
	options, _ := bootstrapPlanFile(t)
	for _, command := range []string{"run-node", "fetch-source"} {
		t.Run(command, func(t *testing.T) {
			boundary := errors.New("service boundary")
			check := func(ctx context.Context, got BootstrapOptions) {
				if ctx != t.Context() || got != options {
					t.Errorf("bootstrap: %#v", got)
				}
			}
			runtime := BootstrapRuntime{
				Source: func(ctx context.Context, got BootstrapOptions) (launcher.SourceInit, error) {
					check(ctx, got)
					return launcher.SourceInit{}, boundary
				},
				Host: func(ctx context.Context, got BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
					check(ctx, got)
					return launcher.BootstrapHost{}, nil, boundary
				},
			}
			if err := runtime.Run(t.Context(), command, bootstrapArguments(options)); !errors.Is(err, boundary) {
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
			if err := (BootstrapRuntime{}).Run(t.Context(), command, args); err == nil {
				t.Errorf("%s accepted %v", command, args)
			}
		}
	}
}
