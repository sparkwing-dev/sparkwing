package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV22_KeepsLegacySecretsAndAdmitsARepoScopedTwin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-repo.db")
	st, err := store.Open(path)
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

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	sec, err := up.GetSecret("DEPLOY_KEY")
	if err != nil {
		t.Fatalf("GetSecret after migration: %v", err)
	}
	if sec.Value != "legacy-value" || sec.Repo != "" {
		t.Errorf("migrated secret = %+v, want the legacy value unscoped", sec)
	}

	later := time.Now().UTC()
	if err := up.CreateOrReplaceSecret(store.Secret{Name: "DEPLOY_KEY", Value: "web-value", Principal: "alice", Repo: "acme/web", Masked: true}, later); err != nil {
		t.Fatalf("CreateOrReplaceSecret repo-scoped: %v", err)
	}
	scoped, err := up.GetSecretForRepo("DEPLOY_KEY", "acme/web")
	if err != nil {
		t.Fatalf("GetSecretForRepo: %v", err)
	}
	if scoped.Value != "web-value" {
		t.Errorf("repo-scoped read = %q, want the repository's own row", scoped.Value)
	}
	unscoped, err := up.GetSecret("DEPLOY_KEY")
	if err != nil {
		t.Fatalf("GetSecret after adding a twin: %v", err)
	}
	if unscoped.Value != "legacy-value" {
		t.Errorf("unscoped read = %q, want the legacy row intact", unscoped.Value)
	}
}

func TestSecrets_RepoScopeResolution(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	for _, s := range []struct{ name, value, repo string }{
		{"SHARED", "shared-value", ""},
		{"DEPLOY_KEY", "web-key", "acme/web"},
		{"DEPLOY_KEY", "api-key", "acme/api"},
	} {
		if err := st.CreateOrReplaceSecret(store.Secret{Name: s.name, Value: s.value, Principal: "alice", Repo: s.repo, Masked: true}, now); err != nil {
			t.Fatalf("CreateOrReplaceSecret %s/%s: %v", s.name, s.repo, err)
		}
	}

	for _, tc := range []struct {
		name    string
		secret  string
		repo    string
		want    string
		wantErr bool
	}{
		{"repo owns the name", "DEPLOY_KEY", "acme/web", "web-key", false},
		{"sibling repo gets its own", "DEPLOY_KEY", "acme/api", "api-key", false},
		{"unscoped is readable by every repo", "SHARED", "acme/web", "shared-value", false},
		{"no unscoped fallback exists", "DEPLOY_KEY", "", "", true},
		{"stranger repo has no row", "DEPLOY_KEY", "other/app", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.GetSecretForRepo(tc.secret, tc.repo)
			if tc.wantErr {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("GetSecretForRepo(%q, %q) err = %v, want ErrNotFound", tc.secret, tc.repo, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetSecretForRepo(%q, %q): %v", tc.secret, tc.repo, err)
			}
			if got.Value != tc.want {
				t.Errorf("GetSecretForRepo(%q, %q) = %q, want %q", tc.secret, tc.repo, got.Value, tc.want)
			}
		})
	}

	if err := st.DeleteSecret("DEPLOY_KEY", "acme/web"); err != nil {
		t.Fatalf("DeleteSecret scoped: %v", err)
	}
	if _, err := st.GetSecretForRepo("DEPLOY_KEY", "acme/api"); err != nil {
		t.Errorf("deleting one repo's row removed a sibling's: %v", err)
	}
}

func TestSecrets_UnscopedRowAnswersARunOnlyWhenShared(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
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

	if _, err := st.GetSecretForRun("LEGACY", "acme/web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSecretForRun(LEGACY) err = %v, want ErrNotFound for an unshared unscoped row", err)
	}
	if sec, err := st.GetSecretForRepo("LEGACY", "acme/web"); err != nil || sec.Value != "legacy" {
		t.Errorf("GetSecretForRepo(LEGACY) = (%v, %v), want the row for an admin reader", sec, err)
	}
	sec, err := st.GetSecretForRun("NPM_TOKEN", "acme/web")
	if err != nil {
		t.Fatalf("GetSecretForRun(NPM_TOKEN): %v", err)
	}
	if sec.Value != "npm" || !sec.Shared {
		t.Errorf("GetSecretForRun(NPM_TOKEN) = %+v, want the shared row", sec)
	}
}

func TestSchemaV23_ExistingSecretsDefaultToUnshared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared-migration.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE secrets DROP COLUMN shared`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := st.DB().Exec(storetest.Rebind(st, `
        INSERT INTO secrets (name, value, principal, created_at, updated_at, masked, repo)
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

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	if _, err := up.GetSecretForRun("LEGACY", "acme/web"); !errors.Is(err, store.ErrNotFound) {
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

func TestRepoForClaimedRun_NamesTheRepoOfTheRunTheCallerIsExecuting(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	now := time.Now()
	runner := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}
	stranger := store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}
	for _, r := range []struct{ id, repo string }{{"run-web", "acme/web"}, {"run-api", "acme/api"}} {
		if err := st.CreateRun(ctx, store.Run{
			ID: r.id, Pipeline: "deploy", Status: "running", Repo: r.repo, StartedAt: now,
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

	if _, err := st.RepoForClaimedRun(ctx, "run-web", runner, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RepoForClaimedRun with no claim err = %v, want ErrNotFound", err)
	}
	if repos, err := st.ReposForClaimant(ctx, runner, now); err != nil || len(repos) != 0 {
		t.Fatalf("ReposForClaimant with no claim = (%v, %v), want none", repos, err)
	}

	for range 2 {
		if _, err := st.ClaimNextReadyNode(ctx, runner, "holder-a", time.Minute, nil); err != nil {
			t.Fatalf("ClaimNextReadyNode: %v", err)
		}
	}

	for _, tc := range []struct{ run, want string }{
		{"run-web", "acme/web"},
		{"run-api", "acme/api"},
	} {
		repo, err := st.RepoForClaimedRun(ctx, tc.run, runner, now)
		if err != nil {
			t.Fatalf("RepoForClaimedRun(%s): %v", tc.run, err)
		}
		if repo != tc.want {
			t.Errorf("RepoForClaimedRun(%s) = %q, want %q", tc.run, repo, tc.want)
		}
	}
	repos, err := st.ReposForClaimant(ctx, runner, now)
	if err != nil {
		t.Fatalf("ReposForClaimant: %v", err)
	}
	if len(repos) != 2 {
		t.Errorf("ReposForClaimant = %v, want both repositories so the caller must name its run", repos)
	}
	if _, err := st.RepoForClaimedRun(ctx, "run-web", stranger, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a principal holding nothing resolved run-web (err %v)", err)
	}
}

func TestRepoForClaimedRun_AcceptsTheTriggerClaim(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "trigger-claims.db"))
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
		ID: "run-web", Pipeline: "deploy", Status: "pending", Repo: "acme/web", StartedAt: now,
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
	repo, err := st.RepoForClaimedRun(ctx, "run-web", dispatcher, now)
	if err != nil {
		t.Fatalf("RepoForClaimedRun: %v", err)
	}
	if repo != "acme/web" {
		t.Errorf("RepoForClaimedRun = %q, want acme/web", repo)
	}
	other := store.ClaimIdentity{Principal: "pool", TokenPrefix: "swr_other"}
	if held, err := st.PrincipalHoldsTriggerClaim(ctx, "run-web", other, now); err != nil || held {
		t.Errorf("a second token sharing the principal held the claim (%v, %v)", held, err)
	}
}
