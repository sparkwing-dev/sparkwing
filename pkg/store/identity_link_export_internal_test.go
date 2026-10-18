package store

import (
	"context"
	"time"
)

func (s *Store) LinkIdentity(ctx context.Context, accountID string, p SignInProfile, now time.Time) (Identity, error) {
	var out Identity
	attempt := func() error {
		return s.inTx(ctx, func(tx *storeTx) error {
			var err error
			out, err = linkIdentityTx(ctx, tx, accountID, p, now)
			return err
		})
	}
	err := attempt()
	if isUniqueViolation(err) {
		err = attempt()
	}
	return out, err
}
