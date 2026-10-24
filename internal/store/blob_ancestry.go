package store

import "context"

// nodeIsAncestor checks a job's transitive dependencies in the tenant's sealed specs.
func (t *Tenant) nodeIsAncestor(ctx context.Context, runID, ancestor, node string) (found bool, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		dependencies, _, err := blobDependenciesTx(ctx, tx, t.team, runID, node)
		found = dependencies[ancestor]
		return err
	})
	return found, err
}
