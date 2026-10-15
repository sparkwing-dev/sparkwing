package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ResolveBlobAuthority derives live scope and uploader identity from persisted authority.
func (t *Tenant) ResolveBlobAuthority(ctx context.Context, selector blob.Authority, grant capability.Grant) (blob.Authority, error) {
	if grant != capability.DataRead && grant != capability.DataWrite {
		return blob.Authority{}, authorityError(apierr.Forbidden, "data grant is required")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return blob.Authority{}, err
	}
	defer rollbackOrLog(tx)
	actor, err := resolveBlobAuthorityTx(ctx, tx, t.team, selector, grant)
	if err != nil {
		return blob.Authority{}, err
	}
	if err := tx.Commit(); err != nil {
		return blob.Authority{}, err
	}
	return actor.authority, nil
}
