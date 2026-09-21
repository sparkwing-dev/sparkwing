package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV22_KeepsLegacySecretsAndAdmitsAScopedTwin(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE secrets`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`
        CREATE TABLE secrets (
            name       TEXT PRIMARY KEY,
            value      TEXT NOT NULL,
            principal  TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            updated_at INTEGER NOT NULL,
            masked     INTEGER NOT NULL DEFAULT 1
        )`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := st.DB().Exec(storetest.Rebind(st, `
        INSERT INTO secrets (name, value, principal, created_at, updated_at, masked)
        VALUES (?, ?, ?, ?, ?, 1)`),
		"DEPLOY_KEY", "legacy-value", "alice", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 22`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	sec, err := up.GetSecret("DEPLOY_KEY")
	if err != nil {
		t.Fatalf("GetSecret after migration: %v", err)
	}
	if sec.Value != "legacy-value" || sec.Pipeline != "" {
		t.Errorf("migrated secret = %+v, want the legacy value unscoped", sec)
	}

	later := time.Now().UTC()
	if err := up.CreateOrReplaceSecret(store.Secret{
		Name: "DEPLOY_KEY", Value: "web-value", Principal: "alice", Pipeline: "deploy-web", Masked: true,
	}, later); err != nil {
		t.Fatalf("CreateOrReplaceSecret pipeline-scoped: %v", err)
	}
	scoped, err := up.GetSecretForPipeline("DEPLOY_KEY", "deploy-web")
	if err != nil {
		t.Fatalf("GetSecretForPipeline: %v", err)
	}
	if scoped.Value != "web-value" {
		t.Errorf("pipeline-scoped read = %q, want the pipeline's own row", scoped.Value)
	}
	unscoped, err := up.GetSecret("DEPLOY_KEY")
	if err != nil {
		t.Fatalf("GetSecret after adding a twin: %v", err)
	}
	if unscoped.Value != "legacy-value" {
		t.Errorf("unscoped read = %q, want the legacy row intact", unscoped.Value)
	}
}

// A store carrying repository-scoped rows comes up with the same bytes under
// the pipeline column, so nothing is lost and nothing is widened: the old
// scope value names no pipeline, so it answers no run until an admin re-keys
// it.
func TestSchemaV48_RepoScopedSecretsBecomePipelineScopedAndAnswerNoRun(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE secrets RENAME COLUMN pipeline TO repo`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE runs RENAME COLUMN declared_repo TO repo`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := st.DB().Exec(storetest.Rebind(st, `
        INSERT INTO secrets (name, value, principal, created_at, updated_at, masked, repo, shared)
        VALUES (?, ?, ?, ?, ?, 1, ?, 0)`),
		"DEPLOY_KEY", "web-key", "alice", now, now, "acme/web",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 48`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`DELETE FROM sparkwing_requirements WHERE name IN (?, ?)`),
		"pipeline-scoped-secrets", "declared-run-repo"); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	row, err := up.GetSecretRow("DEPLOY_KEY", "acme/web")
	if err != nil {
		t.Fatalf("GetSecretRow after migration: %v", err)
	}
	if row.Value != "web-key" || row.Pipeline != "acme/web" {
		t.Errorf("migrated row = %+v, want the value kept under the old scope string", row)
	}
	if _, err := up.GetSecretForRun("DEPLOY_KEY", "deploy"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a repository-scoped row answered pipeline deploy (err %v), want ErrNotFound", err)
	}
}

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
		if err := st.CreateOrReplaceSecret(store.Secret{
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
			got, err := st.GetSecretForPipeline(tc.secret, tc.pipeline)
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

	if err := st.DeleteSecret("DEPLOY_KEY", "deploy-web"); err != nil {
		t.Fatalf("DeleteSecret scoped: %v", err)
	}
	if _, err := st.GetSecretForPipeline("DEPLOY_KEY", "deploy-api"); err != nil {
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
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "LEGACY", Value: "legacy", Principal: "alice", Masked: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: "NPM_TOKEN", Value: "npm", Principal: "alice", Masked: true, Shared: true,
	}, now); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetSecretForRun("LEGACY", "deploy-web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSecretForRun(LEGACY) err = %v, want ErrNotFound for an unshared unscoped row", err)
	}
	if sec, err := st.GetSecretForPipeline("LEGACY", "deploy-web"); err != nil || sec.Value != "legacy" {
		t.Errorf("GetSecretForPipeline(LEGACY) = (%v, %v), want the row for an admin reader", sec, err)
	}
	sec, err := st.GetSecretForRun("NPM_TOKEN", "deploy-web")
	if err != nil {
		t.Fatalf("GetSecretForRun(NPM_TOKEN): %v", err)
	}
	if sec.Value != "npm" || !sec.Shared {
		t.Errorf("GetSecretForRun(NPM_TOKEN) = %+v, want the shared row", sec)
	}
}

func TestSchemaV23_ExistingSecretsDefaultToUnshared(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE secrets DROP COLUMN shared`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := st.DB().Exec(storetest.Rebind(st, `
        INSERT INTO secrets (name, value, principal, created_at, updated_at, masked, pipeline)
        VALUES (?, ?, ?, ?, ?, 1, '')`),
		"LEGACY", "legacy-value", "alice", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 23`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	if _, err := up.GetSecretForRun("LEGACY", "deploy-web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a migrated secret answered a run without being shared (err %v)", err)
	}
	sec, err := up.GetSecret("LEGACY")
	if err != nil {
		t.Fatalf("GetSecret after migration: %v", err)
	}
	if sec.Shared {
		t.Error("migrated secret came back shared; the upgrade must not widen it")
	}
}

func TestPipelineForClaimedRun_NamesThePipelineOfTheRunTheCallerIsExecuting(t *testing.T) {
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

	if _, err := st.PipelineForClaimedRun(ctx, "run-web", runner, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PipelineForClaimedRun with no claim err = %v, want ErrNotFound", err)
	}
	if pipelines, err := st.PipelinesForClaimant(ctx, runner, now); err != nil || len(pipelines) != 0 {
		t.Fatalf("PipelinesForClaimant with no claim = (%v, %v), want none", pipelines, err)
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
		pipeline, err := st.PipelineForClaimedRun(ctx, tc.run, runner, now)
		if err != nil {
			t.Fatalf("PipelineForClaimedRun(%s): %v", tc.run, err)
		}
		if pipeline != tc.want {
			t.Errorf("PipelineForClaimedRun(%s) = %q, want %q", tc.run, pipeline, tc.want)
		}
	}
	pipelines, err := st.PipelinesForClaimant(ctx, runner, now)
	if err != nil {
		t.Fatalf("PipelinesForClaimant: %v", err)
	}
	if len(pipelines) != 2 {
		t.Errorf("PipelinesForClaimant = %v, want both pipelines so the caller must name its run", pipelines)
	}
	if _, err := st.PipelineForClaimedRun(ctx, "run-web", stranger, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a principal holding nothing resolved run-web (err %v)", err)
	}
}

func TestPipelineForClaimedRun_AcceptsTheTriggerClaim(t *testing.T) {
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
	pipeline, err := st.PipelineForClaimedRun(ctx, "run-web", dispatcher, now)
	if err != nil {
		t.Fatalf("PipelineForClaimedRun: %v", err)
	}
	if pipeline != "deploy" {
		t.Errorf("PipelineForClaimedRun = %q, want deploy", pipeline)
	}
	other := store.ClaimIdentity{Principal: "pool", TokenPrefix: "swr_other"}
	if held, err := st.PrincipalHoldsTriggerClaim(ctx, "run-web", other, now); err != nil || held {
		t.Errorf("a second token sharing the principal held the claim (%v, %v)", held, err)
	}
}
