package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// ResolveAuthority reads live ancestry and membership in one snapshot.
// Route authorization and unit binding liveness remain the caller's checks.
func (o *Operator) ResolveAuthority(ctx context.Context, ref capability.AuthorityRef) (capability.Principal, error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.Principal{}, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, ref, false)
	if err != nil {
		return capability.Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return capability.Principal{}, err
	}
	return chain[0].principal, nil
}

// LookupCapability resolves a bearer digest and its live ancestry in one snapshot.
// It does not authorize a route or check whether the bound unit still holds its lease.
func (o *Operator) LookupCapability(ctx context.Context, digest [32]byte) (capability.Principal, error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.Principal{}, err
	}
	defer rollbackOrLog(tx)
	var prefix string
	err = tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE digest=?`, hex.EncodeToString(digest[:])).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Principal{}, authorityError(apierr.Unauthenticated, "capability is unavailable")
	}
	if err != nil {
		return capability.Principal{}, err
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
	if err != nil {
		return capability.Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return capability.Principal{}, err
	}
	return chain[0].principal, nil
}

// ExchangeCapability consumes a bootstrap and returns its replacement only after commit.
func (o *Operator) ExchangeCapability(ctx context.Context, spec capability.ExchangeSpec) (capability.Issued, error) {
	return o.s.commitIssuance(ctx, capability.IssueRequest{Kind: capability.Exchange, Exchange: &spec})
}
