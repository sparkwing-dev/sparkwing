package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func bindingSecretTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if c.Binding.RunID == "" || target.SecretName == "" || target.RunID != "" && target.RunID != c.Binding.RunID {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), c.Binding.RunID)
	if err != nil {
		return err
	}
	if run.Spec == nil || !slices.Contains(run.Spec.Resolved.SecretNames, target.SecretName) {
		return bindingRefusal()
	}
	var pool string
	err = tx.QueryRowContext(ctx, `SELECT only_on FROM secrets WHERE team=? AND name=? AND (pipeline=? OR (pipeline='' AND shared=1)) ORDER BY CASE WHEN pipeline='' THEN 1 ELSE 0 END LIMIT 1`, c.Team, target.SecretName, run.Pipeline).Scan(&pool)
	if err != nil {
		return err
	}
	if pool != "" && (run.Request.Request.Options.Runner.Set && run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere || !slices.Contains(c.ReservedPools, pool) || !slices.Contains(run.AllowedPools, pool)) {
		return bindingRefusal()
	}
	return nil
}
