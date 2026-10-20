package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func registerGitHubEnrollment(t *testing.T, s *store.Store, tenant *store.Tenant, row store.GitHubRunnerBinding) (store.GitHubRunnerBinding, error) {
	t.Helper()
	identity := "enrollment-" + string(tenant.Team())
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1) ON CONFLICT(id) DO NOTHING`, []any{identity, identity + "@example.test"}},
		{`INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,?,'owner',1) ON CONFLICT(team,account_id) DO NOTHING`, []any{tenant.Team(), identity}},
		{`INSERT INTO sessions(hash,principal,scopes,team,account_id,created_at,expires_at) VALUES (?,?,'',?,?,1,?) ON CONFLICT(hash) DO NOTHING`, []any{identity, identity, tenant.Team(), identity, time.Now().Add(time.Hour).UnixNano()}},
		{`INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,?,0) ON CONFLICT DO NOTHING`, []any{identity, tenant.Team()}},
	} {
		if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, statement.query), statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: identity}}, Target: capability.Target{Team: string(tenant.Team())}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	return tenant.RegisterGitHubRunner(t.Context(), a, row)
}
