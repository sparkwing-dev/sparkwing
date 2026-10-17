package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (t *Tenant) mutationFixtureAuthority(test testing.TB, actor string, grant capability.Grant, at time.Time) capability.Authorized {
	test.Helper()
	previous, previousPinned := t.s.clock, t.s.pinnedClock
	t.s.clock = func() time.Time { return at }
	t.s.pinnedClock = t.s.clock
	test.Cleanup(func() { t.s.clock, t.s.pinnedClock = previous, previousPinned })
	if t.s.csrfCipher == nil {
		cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
		if err != nil {
			test.Fatal(err)
		}
		t.s.SetSessionKeyCipher(cipher)
	}
	account, err := t.s.Account(test.Context(), actor)
	if err != nil {
		test.Fatal(err)
	}
	raw, _, _, err := t.s.CreateAccountSession(test.Context(), account, t.team, time.Hour)
	if err != nil {
		test.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	return capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}}, Target: capability.Target{Team: string(t.team)}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{grant}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
}

// InviteForTest exercises the committing invitation adapter with a fresh account session.
func (t *Tenant) InviteForTest(test testing.TB, ctx context.Context, actor, email string, role Role, at time.Time) (Invitation, error) {
	test.Helper()
	a := t.mutationFixtureAuthority(test, actor, capability.TeamAdmin, at)
	key, err := newIdentityID()
	if err != nil {
		return Invitation{}, err
	}
	return t.CreateAuthorizedInvitation(ctx, a, email, role, key)
}

// WithdrawInvitationForTest exercises invitation withdrawal under a current team owner.
func (t *Tenant) WithdrawInvitationForTest(test testing.TB, ctx context.Context, id string, at time.Time) error {
	test.Helper()
	var owner string
	if err := t.s.queryRow(ctx, `SELECT account_id FROM memberships WHERE team=? AND role='owner' ORDER BY account_id LIMIT 1`, t.team).Scan(&owner); err != nil {
		return err
	}
	return t.DeleteAuthorizedInvitation(ctx, t.mutationFixtureAuthority(test, owner, capability.TeamAdmin, at), id)
}

// DeleteTeamForTest exercises owner deletion and reads the committed revocation result.
func (t *Tenant) DeleteTeamForTest(test testing.TB, ctx context.Context, actor string, at time.Time) (TeamDeletion, []string, error) {
	test.Helper()
	out, err := t.DeleteAuthorizedTeam(ctx, t.mutationFixtureAuthority(test, actor, capability.TeamAdmin, at))
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	rows, err := t.s.query(ctx, `SELECT prefix FROM capabilities WHERE team=? AND revoked_at IS NOT NULL`, t.team)
	if err != nil {
		return TeamDeletion{}, nil, err
	}
	prefixes, err := scanPrefixes(rows)
	return out, prefixes, err
}

// RemoveRunForTest removes a fixture and its retained success pin.
func (t *Tenant) RemoveRunForTest(ctx context.Context, id string) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := assertRunBelongsToTeamTx(ctx, tx, t.team, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_success_pins WHERE team=? AND run_id=?`, t.team, id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE team=? AND id=?`, t.team, id)
		return credentialRowChanged(result, err)
	})
}

// MemberCapabilityForTest provisions an account root through its live session.
func (t *Tenant) MemberCapabilityForTest(test testing.TB, actor string, at time.Time) capability.Issued {
	test.Helper()
	a := t.mutationFixtureAuthority(test, actor, capability.RunsWrite, at)
	issued, err := t.s.ProvisionCapability(test.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &a.Principal.Authority, Team: string(t.team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: actor}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}})
	if err != nil {
		test.Fatal(err)
	}
	return issued
}
