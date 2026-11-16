package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTeamDeleteRequiresConfirmation(t *testing.T) {
	if _, err := clisurface.Parse("sparkwing", []string{"team", "delete", "--profile", "work"}); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("team delete without --confirm = %v, want a usage error naming --confirm", err)
	}
}

func TestTeamDeleteCLIThroughAuthenticatedControllerAndStore(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	var server *controller.Server
	state, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"), store.WithPolicies(func(state *store.Store) (store.Policies, error) {
		server = controller.New(state, nil).WithSecretsCipher(cipher)
		return server.StorePolicies(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	teststore.SeedTeam(t, state, "acme")
	teststore.SeedTeam(t, state, "keep")
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('owner','owner@example.test',1,1),('editor','editor@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('acme','owner','owner',1),('keep','owner','owner',1),('acme','editor','editor',1)`,
	} {
		if _, err := teststore.SQL(t, state).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	token := func(account string, role store.Role) string {
		session, _, _, err := state.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: account}, "acme", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(session))
		authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
		issued, err := state.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
			Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &authority, Team: "acme",
			Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account},
			Grants:  teststore.CredentialGrants(store.ScopesForRole(role)), Binding: capability.Binding{Kind: capability.BindingNone},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Raw
	}
	tokens := map[string]string{"owner": token("owner", store.RoleOwner), "editor": token("editor", store.RoleEditor)}
	handler := server.Handler()
	deleteTeam := func(profile string, args ...string) (string, error) {
		inv, err := clisurface.Parse("sparkwing", append([]string{"team", "delete", "--profile", profile}, args...))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runTeamDeleteCommand(t.Context(), inv, &out, func(_ context.Context, profile string) (controllerwire.TeamDeleter, func(), error) {
			deleter, err := client.NewTeamDeleter(client.EngineOptions{URL: "https://controller.test", Bearer: func(context.Context) (string, error) { return tokens[profile], nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w.Result(), nil
			})}})
			return deleter, func() {}, err
		})
		return out.String(), err
	}
	deletionState := func() string {
		var value string
		if err := teststore.SQL(t, state).QueryRowContext(t.Context(), `SELECT COALESCE((SELECT state FROM team_deletions WHERE slug='acme'),'')`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}

	if out, err := deleteTeam("editor", "--confirm", "acme"); err == nil || out != "" {
		t.Fatalf("an editor deleting the team = %q %v, want a refusal", out, err)
	}
	if out, err := deleteTeam("owner", "--confirm", "keep"); err == nil || out != "" {
		t.Fatalf("confirming another team's slug = %q %v, want a refusal", out, err)
	}
	if got := deletionState(); got != "" {
		t.Fatalf("refused requests left deletion state %q", got)
	}

	out, err := deleteTeam("owner", "--confirm", "acme", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var deletion controllerwire.TeamDeletion
	if err := json.Unmarshal([]byte(out), &deletion); err != nil || deletion.Slug != "acme" || deletion.State != store.TeamDeletionPending || deletion.RequestedAt == 0 {
		t.Fatalf("deletion receipt = %q %v", out, err)
	}
	if out, err := deleteTeam("owner", "--confirm", "acme"); err == nil || out != "" {
		t.Fatalf("the revoked profile token deleted again: %q %v", out, err)
	}

	server.ProcessTeamDeletions(t.Context(), time.Now().Add(5*time.Minute))
	if got := deletionState(); got != store.TeamDeletionDone {
		t.Fatalf("deletion state after the purge pass = %q, want done", got)
	}
	var acme, keep int
	if err := teststore.SQL(t, state).QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM memberships WHERE team='acme'), (SELECT COUNT(*) FROM memberships WHERE team='keep')`).Scan(&acme, &keep); err != nil {
		t.Fatal(err)
	}
	if acme != 0 || keep != 1 {
		t.Fatalf("memberships after deletion: acme=%d keep=%d, want 0 and 1", acme, keep)
	}
}

func TestTeamDeletePrintsTheSlugOrTheRevokedProfile(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []string
	}{
		{"plain", []string{"acme\n"}},
		{"pretty", []string{"acme", "pending", "--profile work"}},
	} {
		inv, err := clisurface.Parse("sparkwing", []string{"team", "delete", "--profile", "work", "--confirm", "acme", "-o", tc.format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runTeamDeleteCommand(t.Context(), inv, &out, func(context.Context, string) (controllerwire.TeamDeleter, func(), error) {
			return fakeTeamDeleter{}, func() {}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s output %q lacks %q", tc.format, out.String(), want)
			}
		}
	}
}

type fakeTeamDeleter struct{}

func (fakeTeamDeleter) DeleteTeam(_ context.Context, slug string) (controllerwire.TeamDeletion, error) {
	return controllerwire.TeamDeletion{Slug: slug, State: "pending", RequestedAt: 1}, nil
}
