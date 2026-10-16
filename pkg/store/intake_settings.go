package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// IntakeSettings reads deployment guards and funding from the admission transaction.
func IntakeSettings(ctx context.Context, resolver capability.AuthorityResolver, team Team) (ComputeLimits, bool, error) {
	tx, ok := resolver.(*storeTx)
	if !ok {
		return ComputeLimits{}, false, authorityError(apierr.Unavailable, "intake settings require the admission transaction")
	}
	guards, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return ComputeLimits{}, false, err
	}
	if !holdsFreeAllowance(team) {
		return guards, true, nil
	}
	funded, err := teamFundedTx(ctx, tx, team)
	return guards, funded, err
}
