package store_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func githubAppAuthorization(t *testing.T, s *store.Store, tenant *store.Tenant) capability.Authorized {
	t.Helper()
	const actor = "github-admin"
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?, ?,1,1) ON CONFLICT(id) DO NOTHING`, []any{actor, actor + "@example.test"}},
		{`INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,?,'owner',1) ON CONFLICT(team,account_id) DO NOTHING`, []any{tenant.Team(), actor}},
	} {
		if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, seed.query), seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	authority := membershipActor(t, s, actor, tenant.Team()).Authority
	return capability.Authorized{Principal: capability.Principal{Authority: authority}, Target: capability.Target{Team: string(tenant.Team())}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
}

func githubExtraIDs(extras []string) map[string]int64 {
	ids := map[string]int64{"acme/app": 701}
	for _, extra := range extras {
		extra = strings.ToLower(extra)
		if ids[extra] == 0 {
			ids[extra] = int64(len(ids) + 1)
		}
	}
	return ids
}
