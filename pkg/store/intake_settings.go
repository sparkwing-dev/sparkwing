package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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

// IntakeQueueDepth reads pending work from the admission transaction.
// ChildRootRequest returns the sealed request of the root run a child descends from,
// whose admission budgets the child spends.
func ChildRootRequest(ctx context.Context, resolver capability.AuthorityResolver, team Team, parent string) (runspec.SealedRequest, error) {
	tx, ok := resolver.(*storeTx)
	if !ok {
		return runspec.SealedRequest{}, authorityError(apierr.Unavailable, "child root requires the admission transaction")
	}
	run, err := readRunTx(ctx, tx, team, parent)
	if err != nil || run.Lineage.RootRunID == "" {
		return run.Request, err
	}
	root, err := readRunTx(ctx, tx, team, run.Lineage.RootRunID)
	return root.Request, err
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
