package controller

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimEmptyCountsPollsThatMatchNothingByClaimant(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	e.Store.SetClaimProfileResolver(engineProfileFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	launcher := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	runner := engineHTTPRoot(t, e, "a", "runner", []capability.Grant{capability.NodesClaim})
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	series := func(claimant string) string { return `sparkwing_claim_empty_total{claimant="` + claimant + `"}` }
	before := scrapeRegistry(t)
	engineClaimHTTP(t, h, launcher, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["plan","eval","node"],"available_cores":1}`, 200)
	engineClaimHTTP(t, h, launcher, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["plan","eval","node"],"available_cores":1}`, 200)
	engineClaimHTTP(t, h, runner, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	after := scrapeRegistry(t)
	for claimant, want := range map[string]float64{"launcher": 2, "runner": 1, "here": 0} {
		if got := mustSample(t, after, series(claimant)) - mustSample(t, before, series(claimant)); got != want {
			t.Errorf("%s empty polls = %v, want %v", claimant, got, want)
		}
	}
}
