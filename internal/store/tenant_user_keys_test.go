package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSecretNameIsUniquePerTeam(t *testing.T) {
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	now := time.Now()

	for _, pipeline := range []string{"", "ci"} {
		if err := teststore.SeedSecret(st, alpha.Team(), store.Secret{
			Name: "DEPLOY_KEY", Value: "alpha-" + pipeline, Pipeline: pipeline, Shared: true,
		}, now); err != nil {
			t.Fatalf("alpha DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
		if err := teststore.SeedSecret(st, beta.Team(), store.Secret{
			Name: "DEPLOY_KEY", Value: "beta-" + pipeline, Pipeline: pipeline, Shared: true,
		}, now); err != nil {
			t.Fatalf("beta DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
	}

	for _, c := range []struct {
		tn   *store.Tenant
		want string
	}{{alpha, "alpha-ci"}, {beta, "beta-ci"}} {
		got, err := c.tn.GetSecretRow("DEPLOY_KEY", "ci")
		if err != nil {
			t.Fatalf("%s GetSecretRow: %v", c.tn.Team(), err)
		}
		if got.Value != c.want {
			t.Errorf("%s reads DEPLOY_KEY = %q, want %q", c.tn.Team(), got.Value, c.want)
		}
	}

	rows, err := alpha.ListSecrets()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Value != "alpha-" && row.Value != "alpha-ci" {
			t.Errorf("alpha's listing carries %q, which is not alpha's", row.Value)
		}
	}

	if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, `DELETE FROM secrets WHERE team=? AND name=? AND pipeline=?`), alpha.Team(), "DEPLOY_KEY", "ci"); err != nil {
		t.Fatalf("alpha DeleteSecret: %v", err)
	}
	if _, err := alpha.GetSecretRow("DEPLOY_KEY", "ci"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alpha reads its deleted secret: %v", err)
	}
	got, err := beta.GetSecretRow("DEPLOY_KEY", "ci")
	if err != nil {
		t.Fatalf("beta lost its secret to alpha's delete: %v", err)
	}
	if got.Value != "beta-ci" {
		t.Errorf("beta's secret = %q after alpha deleted its own", got.Value)
	}
}

func TestPipelineProfileIsUniquePerTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")

	if err := alpha.UpsertProfilePin(ctx, "ci", "build", 4, 4096); err != nil {
		t.Fatalf("alpha UpsertProfilePin: %v", err)
	}
	if err := beta.UpsertProfilePin(ctx, "ci", "build", 1, 1024); err != nil {
		t.Fatalf("beta UpsertProfilePin: %v", err)
	}

	for _, c := range []struct {
		tn    *store.Tenant
		cores float64
	}{{alpha, 4}, {beta, 1}} {
		got, err := c.tn.GetPipelineProfile(ctx, "ci", "build")
		if err != nil {
			t.Fatalf("%s GetPipelineProfile: %v", c.tn.Team(), err)
		}
		if got == nil || got.PinnedCores != c.cores {
			t.Errorf("%s reads pinned cores %+v, want %v", c.tn.Team(), got, c.cores)
		}
	}

	if err := alpha.RecordProfileObservation(ctx, "ci", "build", store.ProfileObservation{
		Duration: time.Second, PeakCores: 3, PeakMemoryBytes: 2048, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("alpha RecordProfileObservation: %v", err)
	}
	betaProfile, err := beta.GetPipelineProfile(ctx, "ci", "build")
	if err != nil {
		t.Fatal(err)
	}
	if betaProfile.SampleCount != 0 {
		t.Errorf("beta's profile took alpha's sample: %+v", betaProfile)
	}

	if _, err := alpha.ResetAllProfiles(ctx); err != nil {
		t.Fatalf("alpha ResetAllProfiles: %v", err)
	}
	betaProfile, err = beta.GetPipelineProfile(ctx, "ci", "build")
	if err != nil {
		t.Fatal(err)
	}
	if betaProfile == nil || betaProfile.PinnedCores != 1 {
		t.Errorf("beta's pin = %+v after alpha reset every profile of its own team", betaProfile)
	}
	list, err := beta.ListPipelineProfiles(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PinnedCores != 1 {
		t.Errorf("beta's listing = %+v, want only its own profile", list)
	}
}
