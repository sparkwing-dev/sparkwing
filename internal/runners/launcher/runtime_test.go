package launcher

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type runtimeWire struct {
	controllerwire.Controller
	claims int
}

func (w *runtimeWire) Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	w.claims++
	return controllerwire.ClaimResponse{}, nil
}

func TestLauncherCompositionSelectsLauncherTransport(t *testing.T) {
	httpClient := &http.Client{}
	wire := &runtimeWire{}
	base, kube, boundary, _ := fixture()
	cfg := RuntimeConfig{
		Kube: kube, Boundary: boundary,
		Jobs: base.Config, Demand: controllerwire.ClaimRequest{RunnerVersion: "v0.20.0", Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}},
		HTTP: httpClient, Bearer: func(context.Context) (string, error) { return "launcher-root", nil },
		NewController: func(options client.EngineOptions) (LauncherController, error) {
			bearer, err := options.Bearer(t.Context())
			if err != nil || bearer != "launcher-root" || !options.Launcher || options.HTTP != httpClient || options.URL != base.Config.ControllerURL {
				t.Fatal("launcher transport lost its route selection or credentials")
			}
			return wire, nil
		},
	}
	launcher, err := NewLauncher(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if awarded, err := launcher.LaunchOne(t.Context()); awarded || err != nil || wire.claims != 1 {
		t.Fatalf("award=%v claims=%d error=%v", awarded, wire.claims, err)
	}
	failure := errors.New("transport failed")
	cfg.NewController = func(client.EngineOptions) (LauncherController, error) { return nil, failure }
	if _, err := NewLauncher(t.Context(), cfg); !errors.Is(err, failure) {
		t.Fatalf("lost transport error: %v", err)
	}
}
