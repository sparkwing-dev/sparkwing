package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestSecrets_PipelineScopeResolution(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	for _, s := range []struct{ name, value, pipeline string }{
		{"SHARED", "shared-value", ""},
		{"DEPLOY_KEY", "web-key", "deploy-web"},
		{"DEPLOY_KEY", "api-key", "deploy-api"},
	} {
		if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateOrReplaceSecret(store.Secret{
			Name: s.name, Value: s.value, Principal: "alice", Pipeline: s.pipeline, Masked: true,
		}, now); err != nil {
			t.Fatalf("CreateOrReplaceSecret %s/%s: %v", s.name, s.pipeline, err)
		}
	}

	for _, tc := range []struct {
		name     string
		secret   string
		pipeline string
		want     string
		wantErr  bool
	}{
		{"pipeline owns the name", "DEPLOY_KEY", "deploy-web", "web-key", false},
		{"sibling pipeline gets its own", "DEPLOY_KEY", "deploy-api", "api-key", false},
		{"unscoped is readable by every pipeline", "SHARED", "deploy-web", "shared-value", false},
		{"no unscoped fallback exists", "DEPLOY_KEY", "", "", true},
		{"stranger pipeline has no row", "DEPLOY_KEY", "other-app", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForPipeline(tc.secret, tc.pipeline)
			if tc.wantErr {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("GetSecretForPipeline(%q, %q) err = %v, want ErrNotFound", tc.secret, tc.pipeline, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetSecretForPipeline(%q, %q): %v", tc.secret, tc.pipeline, err)
			}
			if got.Value != tc.want {
				t.Errorf("GetSecretForPipeline(%q, %q) = %q, want %q", tc.secret, tc.pipeline, got.Value, tc.want)
			}
		})
	}

	if err := teststore.ForTeam(t, st, store.DefaultTeam).DeleteSecret("DEPLOY_KEY", "deploy-web"); err != nil {
		t.Fatalf("DeleteSecret scoped: %v", err)
	}
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForPipeline("DEPLOY_KEY", "deploy-api"); err != nil {
		t.Errorf("deleting one pipeline's row removed a sibling's: %v", err)
	}
}

func TestSecrets_UnscopedRowAnswersARunOnlyWhenShared(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateOrReplaceSecret(store.Secret{
		Name: "LEGACY", Value: "legacy", Principal: "alice", Masked: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateOrReplaceSecret(store.Secret{
		Name: "NPM_TOKEN", Value: "npm", Principal: "alice", Masked: true, Shared: true,
	}, now); err != nil {
		t.Fatal(err)
	}

	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForRun("LEGACY", "deploy-web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSecretForRun(LEGACY) err = %v, want ErrNotFound for an unshared unscoped row", err)
	}
	if sec, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForPipeline("LEGACY", "deploy-web"); err != nil || sec.Value != "legacy" {
		t.Errorf("GetSecretForPipeline(LEGACY) = (%v, %v), want the row for an admin reader", sec, err)
	}
	sec, err := teststore.ForTeam(t, st, store.DefaultTeam).GetSecretForRun("NPM_TOKEN", "deploy-web")
	if err != nil {
		t.Fatalf("GetSecretForRun(NPM_TOKEN): %v", err)
	}
	if sec.Value != "npm" || !sec.Shared {
		t.Errorf("GetSecretForRun(NPM_TOKEN) = %+v, want the shared row", sec)
	}
}

func TestClaimedRunFor_NamesThePipelineOfTheRunTheCallerIsExecuting(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	now := time.Now()
	runner := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}
	stranger := store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}
	for _, r := range []struct{ id, pipeline string }{{"run-web", "deploy-web"}, {"run-api", "deploy-api"}} {
		if err := st.CreateRun(ctx, store.Run{
			ID: r.id, Pipeline: r.pipeline, Status: "running", StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: r.id, NodeID: "only", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, r.id, "only"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := st.ClaimedRunFor(ctx, "run-web", runner, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimedRunFor with no claim err = %v, want ErrNotFound", err)
	}

	for range 2 {
		if _, err := st.ClaimNextReadyNode(ctx, runner, "holder-a", time.Minute, nil); err != nil {
			t.Fatalf("ClaimNextReadyNode: %v", err)
		}
	}

	for _, tc := range []struct{ run, want string }{
		{"run-web", "deploy-web"},
		{"run-api", "deploy-api"},
	} {
		pipeline, err := st.ClaimedRunFor(ctx, tc.run, runner, now)
		if err != nil {
			t.Fatalf("ClaimedRunFor(%s): %v", tc.run, err)
		}
		if pipeline.Pipeline != tc.want || pipeline.Team != store.DefaultTeam {
			t.Errorf("ClaimedRunFor(%s) = %+v, want %q in the default team", tc.run, pipeline, tc.want)
		}
	}
	if _, err := st.ClaimedRunFor(ctx, "run-web", stranger, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a principal holding nothing resolved run-web (err %v)", err)
	}
}

func TestClaimedRunFor_AcceptsTheTriggerClaim(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	now := time.Now()
	dispatcher := store.ClaimIdentity{Principal: "pool", TokenPrefix: "swr_pool"}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-web", Pipeline: "deploy", Repo: "acme/web", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "pending", DeclaredRepo: "acme/web", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextTriggerFor(ctx, dispatcher, time.Minute, nil, nil); err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}

	held, err := st.PrincipalHoldsTriggerClaim(ctx, "run-web", dispatcher, now)
	if err != nil || !held {
		t.Fatalf("PrincipalHoldsTriggerClaim = (%v, %v), want true", held, err)
	}
	pipeline, err := st.ClaimedRunFor(ctx, "run-web", dispatcher, now)
	if err != nil {
		t.Fatalf("ClaimedRunFor: %v", err)
	}
	if pipeline.Pipeline != "deploy" {
		t.Errorf("ClaimedRunFor = %+v, want deploy", pipeline)
	}
	other := store.ClaimIdentity{Principal: "pool", TokenPrefix: "swr_other"}
	if held, err := st.PrincipalHoldsTriggerClaim(ctx, "run-web", other, now); err != nil || held {
		t.Errorf("a second token sharing the principal held the claim (%v, %v)", held, err)
	}
}

// The controller reads a runner's secrets in the team ClaimedRunFor names,
// so that team has to be the claimed run's and not the default one.
func TestClaimedRunForNamesTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	now := time.Now()
	if err := acme.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-acme", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-acme", Pipeline: "deploy", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	runner := mintTeamClaimant(t, acme, "agent:acme")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-acme", runner, time.Minute); err != nil {
		t.Fatal(err)
	}
	want := store.ClaimedRun{Team: "acme", Pipeline: "deploy"}
	got, err := st.ClaimedRunFor(ctx, "run-acme", runner, time.Now())
	if err != nil {
		t.Fatalf("ClaimedRunFor: %v", err)
	}
	if got != want {
		t.Errorf("ClaimedRunFor = %+v, want %+v", got, want)
	}
}

// A webhook writes its trigger without a run, and the runner that claims it
// fetches the run's source before the run exists. That claim names the run,
// in the trigger's team, until the run is created; after that the run decides.
func TestClaimedRunFor_CountsATriggerClaimBeforeItsRunExists(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	runner := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}
	stranger := store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run-hook", Pipeline: "deploy-web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimedRunFor(ctx, "run-hook", runner, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("before any claim = %v, want ErrNotFound", err)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-hook", runner, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimedRunFor(ctx, "run-hook", runner, time.Now())
	if err != nil || got.Pipeline != "deploy-web" || got.Team != store.DefaultTeam {
		t.Fatalf("claimant before the run exists = %+v, %v; want the trigger's pipeline and team", got, err)
	}
	if _, err := st.ClaimedRunFor(ctx, "run-hook", stranger, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another claimant = %v, want ErrNotFound", err)
	}
	if _, err := st.ClaimedRunFor(ctx, "run-hook", runner, time.Now().Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after the lease lapsed = %v, want ErrNotFound", err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-hook", Pipeline: "deploy-web", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ClaimedRunFor(ctx, "run-hook", runner, time.Now()); err != nil || got.Pipeline != "deploy-web" {
		t.Fatalf("after the run exists = %+v, %v", got, err)
	}
}
