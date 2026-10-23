package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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

// ChildAdmissionKey returns the charged identity inherited from the parent admission.
func ChildAdmissionKey(ctx context.Context, resolver capability.AuthorityResolver, team Team, parent string) (string, error) {
	tx, ok := resolver.(*storeTx)
	if !ok {
		return "", authorityError(apierr.Unavailable, "child root requires the admission transaction")
	}
	run, err := readRunTx(ctx, tx, team, parent)
	return run.admission.AdmissionKey, err
}

func IntakeQueueDepth(ctx context.Context, resolver capability.AuthorityResolver, team Team) (int, error) {
	tx, ok := resolver.(*storeTx)
	if !ok {
		return 0, authorityError(apierr.Unavailable, "queue depth requires the admission transaction")
	}
	var depth int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triggers WHERE team=? AND status=?`, team, triggerStatusPending).Scan(&depth)
	return depth, err
}
