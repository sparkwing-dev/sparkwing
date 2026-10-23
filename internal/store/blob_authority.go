package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ResolveBlobAuthority derives live scope and uploader identity from persisted authority.
// SourceRead resolves only an unexchanged bootstrap's sealed source input, without uploader identity.
func (t *Tenant) ResolveBlobAuthority(ctx context.Context, selector blob.Authority, grant capability.Grant) (blob.Authority, error) {
	if grant != capability.DataRead && grant != capability.DataWrite && grant != capability.SourceRead {
		return blob.Authority{}, authorityError(apierr.Forbidden, "data grant is required")
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (blob.Authority, error) {
		actor, err := resolveBlobAuthorityTx(ctx, tx, t.team, selector, grant)
		if err != nil {
			return blob.Authority{}, err
		}
		return actor.authority, nil
	})
}
