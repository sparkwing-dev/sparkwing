package store_test

import (
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
		repo := map[string]string{"": "", "ci": "gh:1"}[pipeline]
		if err := teststore.SeedSecret(st, alpha.Team(), store.Secret{
			Name: "DEPLOY_KEY", Value: "alpha-" + pipeline, Repo: repo, Pipeline: pipeline, Shared: repo == "",
		}, now); err != nil {
			t.Fatalf("alpha DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
		if err := teststore.SeedSecret(st, beta.Team(), store.Secret{
			Name: "DEPLOY_KEY", Value: "beta-" + pipeline, Repo: repo, Pipeline: pipeline, Shared: repo == "",
		}, now); err != nil {
			t.Fatalf("beta DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
	}

	for _, c := range []struct {
		tn   *store.Tenant
		want string
	}{{alpha, "alpha-ci"}, {beta, "beta-ci"}} {
		got, err := c.tn.GetSecretRow("DEPLOY_KEY", "gh:1", "ci")
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
	if _, err := alpha.GetSecretRow("DEPLOY_KEY", "gh:1", "ci"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alpha reads its deleted secret: %v", err)
	}
	got, err := beta.GetSecretRow("DEPLOY_KEY", "gh:1", "ci")
	if err != nil {
		t.Fatalf("beta lost its secret to alpha's delete: %v", err)
	}
	if got.Value != "beta-ci" {
		t.Errorf("beta's secret = %q after alpha deleted its own", got.Value)
	}
}
