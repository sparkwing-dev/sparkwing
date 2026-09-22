package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A runner reads the secrets of the team whose run it holds. Both teams
// keep a row under the same name and pipeline, and a shared unscoped row
// under the same name, so a read that resolves in the default team returns
// a value the runner can tell apart from its own team's.
func TestRunnerReadsTheClaimedRunsTeamSecrets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, row := range []struct {
		write       func(store.Secret, time.Time) error
		name, value string
		pipeline    string
		shared      bool
	}{
		{st.CreateOrReplaceSecret, "API_KEY", "default-team-value", "deploy", false},
		{acme.CreateOrReplaceSecret, "API_KEY", "acme-value", "deploy", false},
		{st.CreateOrReplaceSecret, "NPM_TOKEN", "default-npm", "", true},
		{acme.CreateOrReplaceSecret, "NPM_TOKEN", "acme-npm", "", true},
	} {
		if err := row.write(store.Secret{
			Name: row.name, Value: row.value, Principal: "root", Pipeline: row.pipeline,
			Masked: true, Shared: row.shared,
		}, now); err != nil {
			t.Fatalf("seed %s=%s: %v", row.name, row.value, err)
		}
	}
	if err := acme.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-acme", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-acme", Pipeline: "deploy", Status: "pending", StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	raw, _, err := acme.CreateToken(ctx, "agent:acme", store.TokenKindRunner, runnerScopes, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	c := client.NewWithToken(srv.URL, nil, raw)

	if _, err := c.ClaimSpecificTrigger(ctx, "run-acme", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"API_KEY", "acme-value"},
		{"NPM_TOKEN", "acme-npm"},
	} {
		sec, err := c.GetSecretForRun(ctx, tc.name, "run-acme")
		if err != nil {
			t.Fatalf("GetSecretForRun(%s): %v", tc.name, err)
		}
		if sec.Value != tc.want {
			t.Errorf("an acme runner holding an acme run read %s = %q, want %q", tc.name, sec.Value, tc.want)
		}
		// safety: a read that names no run resolves through the claimant's
		// only claimed run, a second path to the same team.
		sec, err = c.GetSecret(ctx, tc.name)
		if err != nil {
			t.Fatalf("GetSecret(%s): %v", tc.name, err)
		}
		if sec.Value != tc.want {
			t.Errorf("an acme runner naming no run read %s = %q, want %q", tc.name, sec.Value, tc.want)
		}
	}
}
