package teststore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ProvisionCredential creates a current-schema root through the real issuer.
func ProvisionCredential(ctx context.Context, st *store.Store, spec capability.ProvisionSpec) (string, *capability.Capability, error) {
	seed := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	bootstrap := capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Seed: seed, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}}
	if _, err := st.EnsureBootstrapCapability(ctx, bootstrap); err != nil {
		return "", nil, err
	}
	snapshot, err := st.ReadAuthentication(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(seed))}, capability.RequestEvidence{})
	if err != nil {
		return "", nil, err
	}
	root := snapshot.Authorities[0].Principal
	if spec.Kind == capability.ProvisionBootstrap {
		return seed, &root.Capability, nil
	}
	ref := root.Authority
	if spec.Kind == capability.ProvisionMember {
		spec.AllowTeamAdmin = true
		if err := seedCredentialMember(ctx, st, spec.Team, spec.Subject.ID, snapshot.Now); err != nil {
			return "", nil, err
		}
		digest := sha256.Sum256([]byte("fixture-session:" + spec.Team + ":" + spec.Subject.ID))
		ref = capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	}
	spec.Authority = &ref
	issued, err := st.ProvisionCapability(ctx, spec)
	if err != nil {
		return "", nil, err
	}
	return issued.Raw, &issued.Capability, nil
}

func seedCredentialMember(ctx context.Context, st *store.Store, team, account string, now time.Time) error {
	digest := sha256.Sum256([]byte("fixture-session:" + team + ":" + account))
	queries := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1) ON CONFLICT(id) DO NOTHING`, []any{account, account + "@fixture.invalid"}},
		{`INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,?,'owner',1) ON CONFLICT(team,account_id) DO NOTHING`, []any{team, account}},
		{`INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id,membership_epoch) VALUES (?,?,?,?,?,?,?,COALESCE((SELECT epoch FROM membership_epochs WHERE team=? AND account_id=?),0)) ON CONFLICT(hash) DO NOTHING`, []any{hex.EncodeToString(digest[:]), account, "runs.read,runs.write,runs.control,team.admin", now.UnixNano(), now.Add(time.Hour).UnixNano(), team, account, team, account}},
	}
	for _, query := range queries {
		q := query.q
		if st.Dialect() == store.DialectPostgres {
			for i := 1; strings.Contains(q, "?"); i++ {
				q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
			}
		}
		if _, err := Exec(ctx, st, q, query.args...); err != nil {
			return err
		}
	}
	return nil
}

// CredentialExpiry converts an explicit fixture lifetime to its final absolute expiry.
func CredentialExpiry(now time.Time, ttl time.Duration) *time.Time {
	if ttl <= 0 {
		return nil
	}
	until := now.Add(ttl)
	return &until
}

// CredentialGrants converts a fixture's explicit scope list to typed grants.
func CredentialGrants(scopes []string) []capability.Grant {
	grants := make([]capability.Grant, len(scopes))
	for i, scope := range scopes {
		grants[i] = capability.Grant(scope)
	}
	return grants
}
