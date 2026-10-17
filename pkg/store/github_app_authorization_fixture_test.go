package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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
		{`INSERT INTO sessions(hash,principal,scopes,team,account_id,created_at,expires_at) VALUES (?,?,'',?,?,?,?) ON CONFLICT(hash) DO NOTHING`, []any{"github-admin-" + string(tenant.Team()), actor, tenant.Team(), actor, time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano()}},
	} {
		if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, seed.query), seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	return capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "github-admin-" + string(tenant.Team())}}, Target: capability.Target{Team: string(tenant.Team())}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
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
