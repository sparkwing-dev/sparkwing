package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ReadAuthentication reads presentation identity, full ancestry, membership and
// target liveness in one committed snapshot with the store clock. Evidence selects
// records but grants no authority. No reusable presentation enters persistence.
func (s *Store) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if lookup.Class == capability.CredentialPeer {
		return s.readPeerAuthentication(ctx, lookup, evidence)
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	ref, err := authenticationAuthorityTx(ctx, tx, lookup)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	chain, err := authorityChainTx(ctx, tx, ref, false)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if chain[0].principal.Capability.Audience != lookup.Audience {
		return capability.AuthenticationSnapshot{}, authorityError(apierr.Unauthenticated, "credential audience does not match")
	}
	out := capability.AuthenticationSnapshot{}
	for _, node := range chain {
		out.Authorities = append(out.Authorities, capability.AuthorityState{Principal: node.principal})
	}
	out.Binding, err = authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if err := authenticationResultTx(ctx, tx, chain[0].principal.Capability, evidence, &out.Binding); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	out.Now = tx.now()
	if err := tx.Commit(); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	return out, nil
}

func (s *Store) readPeerAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if lookup.Peer.Kind != capability.SubjectLocal || lookup.Peer.ID == "" || lookup.Digest != ([32]byte{}) || lookup.Audience != "" || evidence.Target.Team != "" && evidence.Target.Team != string(DefaultTeam) {
		return capability.AuthenticationSnapshot{}, authorityError(apierr.Unauthenticated, "verified local peer required")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	var team string
	if err := (&Tenant{s: s, team: DefaultTeam}).lockTeamTx(ctx, tx); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE name=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=teams.name AND state=?)`, DefaultTeam, TeamDeletionPending).Scan(&team); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability}
	err = tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind=? AND subject_id=? AND parent_id IS NULL ORDER BY created_at,prefix LIMIT 1`, team, lookup.Peer.Kind, lookup.Peer.ID).Scan(&ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		c := capability.Capability{Team: team, Subject: lookup.Peer, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}}
		for _, scope := range ScopesForRole(RoleOwner) {
			c.Grants = append(c.Grants, capability.Grant(scope))
		}
		// safety: the peer root bearer must never leave authentication.
		issued, issueErr := persistIssuedCapabilityTx(ctx, tx, c, "")
		if issueErr != nil {
			return capability.AuthenticationSnapshot{}, issueErr
		}
		ref.ID = issued.Capability.Prefix
	} else if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if p.Capability.Subject != lookup.Peer || p.Capability.Team != team || p.Capability.Parent != nil || p.Capability.Binding != (capability.Binding{Kind: capability.BindingNone}) || p.Capability.Unit != "" || p.Capability.Audience != capability.Controller {
		return capability.AuthenticationSnapshot{}, authorityError(apierr.Unauthenticated, "invalid local peer authority")
	}
	p.Class = capability.CredentialPeer
	out := capability.AuthenticationSnapshot{Now: tx.now(), Authorities: []capability.AuthorityState{{Principal: p}}}
	return out, tx.Commit()
}

func authenticationAuthorityTx(ctx context.Context, tx *storeTx, lookup capability.CredentialLookup) (capability.AuthorityRef, error) {
	var ref capability.AuthorityRef
	var query string
	switch lookup.Class {
	case capability.CredentialBearer:
		ref.Kind, query = capability.AuthorityCapability, `SELECT prefix FROM capabilities WHERE digest=?`
	case capability.CredentialSession:
		ref.Kind, query = capability.AuthoritySession, `SELECT hash FROM sessions WHERE hash=?`
	default:
		return ref, authorityError(apierr.Unauthenticated, "credential class has no persisted authentication authority")
	}
	err := tx.QueryRowContext(ctx, query, hex.EncodeToString(lookup.Digest[:])).Scan(&ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ref, authorityError(apierr.Unauthenticated, "credential is unavailable")
	}
	return ref, err
}

var _ capability.AuthenticationReader = (*Store)(nil)
