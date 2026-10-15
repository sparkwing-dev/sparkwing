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
		out.Authorities = append(out.Authorities, capability.AuthorityState{Principal: node.principal, MembershipValid: true})
	}
	out.Binding, err = authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	out.Now = tx.now()
	if err := tx.Commit(); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	return out, nil
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
