package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	if _, err := st.DB().Exec(`
        INSERT INTO secrets (name, value, principal, created_at, updated_at, masked)
        VALUES (?, ?, ?, ?, ?, 1)`,
		"DEPLOY_KEY", "legacy-value", "alice", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 22`); err != nil {
		t.Fatal(err)
	}
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
	if err := up.CreateOrReplaceSecret("DEPLOY_KEY", "web-value", "alice", "acme/web", true, later); err != nil {
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
		if err := st.CreateOrReplaceSecret(s.name, s.value, "alice", s.repo, true, now); err != nil {
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

func TestRepoForPrincipalClaim_NamesTheRepoOfTheHeldRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	now := time.Now()
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

	if repo, err := st.RepoForPrincipalClaim(ctx, store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}, now); err != nil || repo != "" {
		t.Fatalf("RepoForPrincipalClaim with no claim = (%q, %v), want empty", repo, err)
	}

	n, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}, "holder-a", time.Minute, nil)
	if err != nil {
		t.Fatalf("ClaimNextReadyNode: %v", err)
	}
	want := map[string]string{"run-web": "acme/web", "run-api": "acme/api"}[n.RunID]

	repo, err := st.RepoForPrincipalClaim(ctx, store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner-a"}, now)
	if err != nil {
		t.Fatalf("RepoForPrincipalClaim: %v", err)
	}
	if repo != want {
		t.Errorf("RepoForPrincipalClaim = %q, want %q (the repo of the run it holds)", repo, want)
	}
	if other, err := st.RepoForPrincipalClaim(ctx, store.ClaimIdentity{Principal: "runner-b", TokenPrefix: "swr_runner-b"}, now); err != nil || other != "" {
		t.Errorf("a principal holding nothing resolved repo %q (err %v)", other, err)
	}
}
