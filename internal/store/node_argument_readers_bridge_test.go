package store

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// CheckNodeArgumentBlobMetadataForTest exercises the store readers used by dependency and memo authorization.
func (s *Store) CheckNodeArgumentBlobMetadataForTest(ctx context.Context, team Team, run, node, dependency string) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		deps, _, err := blobDependenciesTx(ctx, tx, team, run, node)
		if err != nil {
			return err
		}
		if !deps[dependency] {
			return fmt.Errorf("dependency declaration lost")
		}
		actor := blobActor{authority: blob.Authority{Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: run, NodeID: node}}}}
		return requireBlobMemoTx(ctx, tx, actor)
	})
}

// ResealNodeArgumentsForTest exercises the rotation transform on a live legacy node row.
func (s *Store) ResealNodeArgumentsForTest(ctx context.Context, team Team, run, node string) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT spec_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, run, node).Scan(&raw); err != nil {
			return err
		}
		cipher, ok := s.runEnvCipher.(SealedRotationCipher)
		if !ok {
			return fmt.Errorf("rotation cipher unavailable")
		}
		r := sealedRotationTx{tx: tx, cipher: cipher}
		stored, err := r.nodeArguments(team, run, node, raw)
		if err != nil {
			return err
		}
		if stored == nil || r.out.PlaintextRunArguments != 1 {
			return fmt.Errorf("legacy node was not sealed")
		}
		_, err = tx.execContext(ctx, `UPDATE nodes SET spec_json=? WHERE team=? AND run_id=? AND node_id=?`, stored, team, run, node)
		return err
	})
}

// ResealScheduleArgumentsForTest applies the rotation transform without changing the store's serving cipher.
func (s *Store) ResealScheduleArgumentsForTest(ctx context.Context, team Team) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		cipher, ok := s.runEnvCipher.(SealedRotationCipher)
		if !ok {
			return fmt.Errorf("rotation cipher unavailable")
		}
		r := sealedRotationTx{tx: tx, cipher: cipher}
		for after, more := "", true; more; {
			var err error
			after, more, err = r.scheduleArguments(ctx, team, after, false)
			if err != nil {
				return err
			}
		}
		return nil
	})
}
