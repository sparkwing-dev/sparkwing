//go:build unix

package controller_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// A plan compile failure must reach run detail, with secret values masked, so `runs status` says why the run failed.
func TestCloudPlanCompileFailureShowsMaskedCompilerOutput(t *testing.T) {
	modules, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(modules)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOPROXY", "off")
	const secret = "plan-compile-secret-5c1e"
	repo, sha, engines, selected := burstSDKSourceFixture(t, "\nvar compileFailure int = \""+secret+"\"\n")
	award := controller.CloudPlanAcceptance(t, repo, sha, burstSDKPipelineName, map[string]string{"token": secret})
	wire, err := client.NewEngineClient(award.Options)
	if err != nil {
		t.Fatal(err)
	}
	private, err := client.NewLocalHostClient(award.Options, award.Unit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	state, err := nodehost.NewRemoteHostStore(wire, private, award.Unit.UnitID, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	state.Finish = private.FinishRunAndUnit
	spec, err := source.ForRequest(award.Unit.Request)
	if err != nil {
		t.Fatal(err)
	}
	access, err := client.NewSourceAccess(award.Options, award.Unit.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	input, err := nodehost.NewRunInput(award.Unit)
	if err != nil {
		t.Fatal(err)
	}
	masker := secrets.NewMasker()
	host := &nodehost.OwnerHost{
		Store: state, Masker: masker, Lease: lease.Keeper{Lease: time.Minute, Interval: 20 * time.Second},
		Renew: func(ctx context.Context, actor runstate.Actor, computing bool) (runstate.LeaseBeat, error) {
			return state.RenewOwner(ctx, actor, computing)
		},
		Wait: func(context.Context, store.RunSnapshot) error { return nil },
		Source: source.Options{
			Workspace: t.TempDir(), Access: access, BundleTransport: award.Options.HTTP.Transport,
			Engines: engines, Target: selected.Target,
			Base: procenv.Base{Ambient: os.Environ(), SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}, Masker: masker},
		},
	}
	if _, err := host.Run(t.Context(), input); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("compile failure was not reported masked: %v", err)
	}
	detail, err := award.Member.ReadRun(t.Context(), award.Unit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != string(runstate.RunFailed) || len(detail.Jobs) != 0 {
		t.Fatalf("plan failure outcome: %+v", detail)
	}
	if text := detail.Failure; !strings.Contains(text, "pipeline.go") || !strings.Contains(text, "cannot use") || strings.Contains(text, secret) || !strings.Contains(text, "***") {
		t.Fatalf("run detail lacks the masked compiler output: %q", text)
	}
}
