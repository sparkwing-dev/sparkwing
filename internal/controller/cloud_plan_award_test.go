package controller

import (
	"context"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CloudPlanAward is a Cloud run's plan unit as its pod holds it after the bootstrap exchange.
type CloudPlanAward struct {
	// Options reach the controller with the plan unit's capability.
	Options client.EngineOptions
	// Member reads the run as its requester.
	Member controllerwire.Controller
	Unit   controllerwire.ClaimedUnit
}

// CloudPlanAcceptance uploads the repository at sha, requests a Cloud run of pipeline with secretArgs, and claims and
// exchanges its plan unit through the launcher routes.
func CloudPlanAcceptance(t *testing.T, repo, sha, pipeline string, secretArgs map[string]string) CloudPlanAward {
	t.Helper()
	s, options, input, memberBearer := sourceUploadFixture(t)
	e := newEngineFixture(s.store, EngineController{RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil }), VersionPolicy: engineVersionPolicy(&engineVersionFake{})})
	s.WithEngineController(e.EngineController)
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	open := func(raw string, launcher bool) controllerwire.Controller {
		t.Helper()
		connection := options
		connection.Bearer = func(context.Context) (string, error) { return raw, nil }
		connection.Launcher = launcher
		c, err := client.NewEngineClient(connection)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "cloud-plan", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "snapshot.bundle")
	sourcetest.Git(t, repo, "bundle", "create", bundle, "HEAD")
	input.Git.SHA, input.Source.Ref = sha, sha
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: bundle, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	input.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	input.Pipeline, input.Source.Entrypoint = pipeline, pipeline
	resolved, err := settings.ResolveRequest(settings.Inputs{Context: settings.Context{Origin: runspec.OriginTrigger, Submitter: true, NumCPU: 4}})
	if err != nil {
		t.Fatal(err)
	}
	input.Dispatch, input.Options = "", resolved
	if len(secretArgs) > 0 {
		input.RequestedArgs = secretArgs
		input.SecretArgs = runspec.Optional[[]string]{Set: true, Value: slices.Sorted(maps.Keys(secretArgs))}
	}
	member := open(memberBearer, false)
	created, err := member.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "cloud-plan"})
	if err != nil {
		t.Fatal(err)
	}
	launcher := open(engineHTTPRoot(t, e, "", "cloud-plan-launcher", []capability.Grant{capability.ClaimsLaunch}), true)
	claimed, err := launcher.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil || claimed.Claim == nil || claimed.Claim.RunID != created.RunID {
		t.Fatalf("launcher plan claim: %+v %v", claimed, err)
	}
	exchanged, err := open(claimed.Claim.Capability, false).Exchange(t.Context())
	if err != nil || exchanged.Claim.Unit != capability.UnitPlan {
		t.Fatalf("plan bootstrap exchange: %+v %v", exchanged, err)
	}
	award := CloudPlanAward{Options: options, Member: member, Unit: exchanged.Claim}
	award.Options.Bearer = func(context.Context) (string, error) { return exchanged.Claim.Capability, nil }
	return award
}
