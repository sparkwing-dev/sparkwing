package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// LauncherRunTerminal proves a terminal run under current deployment launcher authority.
// Missing runs are not proof that a bound node can be retired.
func (o *Operator) LauncherRunTerminal(ctx context.Context, authority capability.AuthorityRef, team, run string) (bool, error) {
	if team == "" || run == "" {
		return false, ErrInvalidInput
	}
	terminal := false
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !capability.CloudLauncher(claimant) {
			return bindingRefusal()
		}
		var status runstate.RunState
		var finished sql.NullInt64
		var repository capability.RepositoryIdentity
		err = tx.QueryRowContext(ctx, `SELECT status,finished_at,repo_key,repo_display FROM runs WHERE team=? AND id=?`, team, run).Scan(&status, &finished, &repository.Key, &repository.Display)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := checkRepositoryClaimScope(chain, repository); err != nil {
			return err
		}
		terminal = finished.Valid && (status == runstate.RunSuccess || status == runstate.RunFailed || status == runstate.RunCancelled || status == runstate.RunDryRun)
		_, err = authorityChainTx(ctx, tx, authority, false)
		return err
	})
	return terminal, err
}
