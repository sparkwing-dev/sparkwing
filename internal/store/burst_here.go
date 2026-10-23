package store

import (
	"context"
	"database/sql"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func hereObserverTx(ctx context.Context, tx *storeTx, p capability.Principal) (bool, error) {
	c := p.Capability
	if c.Binding.RunID == "" || c.Binding.Kind != capability.BindingTrigger {
		return false, nil
	}
	chain, err := authorityChainTx(ctx, tx, p.Authority, false)
	if err != nil {
		return false, err
	}
	here := false
	for _, ancestor := range chain {
		a := ancestor.principal.Capability
		here = here || a.Purpose == capability.PurposeHere && a.Binding.RunID == c.Binding.RunID
	}
	if !here {
		return false, nil
	}
	if c.Purpose == capability.PurposeHere {
		return true, nil
	}
	var prefix string
	err = tx.QueryRowContext(ctx, `SELECT capability_prefix FROM triggers WHERE team=? AND id=? AND generation=? AND claimed_by=? AND lease_expires_at>?`, c.Team, c.Binding.RunID, c.Binding.Generation, c.Binding.Holder, tx.now(ctx).UnixNano()).Scan(&prefix)
	if err == sql.ErrNoRows {
		return false, holderRefusal()
	}
	if err != nil {
		return false, err
	}
	if prefix != c.Prefix {
		return false, holderRefusal()
	}
	return true, nil
}
