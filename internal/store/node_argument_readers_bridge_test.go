package store

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// CheckNodeSpecMetadataReadersForTest exercises the internal metadata readers on a persisted sealed node.
func (s *Store) CheckNodeSpecMetadataReadersForTest(t *testing.T, team Team, run, node string) {
	t.Helper()
	for _, reader := range []struct {
		name string
		read func(*storeTx) error
	}{
		{"dependency", func(tx *storeTx) error {
			deps, _, err := blobDependenciesTx(t.Context(), tx, team, run, node)
			if err == nil && (len(deps) != 1 || !deps["build"]) {
				return fmt.Errorf("dependency declaration changed")
			}
			return err
		}},
		{"memo", func(tx *storeTx) error {
			actor := blobActor{authority: blob.Authority{Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: run, NodeID: node}}}}
			return requireBlobMemoTx(t.Context(), tx, actor)
		}},
		{"child-award", func(tx *storeTx) error {
			id, err := encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)})
			if err != nil {
				return err
			}
			row, err := tx.operator.loadAwardRowTx(t.Context(), tx, capability.AwardSpec{Unit: capability.UnitNode, UnitID: id})
			if err != nil {
				return err
			}
			children, err := awardChildrenTx(t.Context(), tx, row, capability.UnitNode)
			if err == nil && !slices.Equal(children, []nodewire.ChildPipeline{{Pipeline: "child", Repository: row.repo}}) {
				return fmt.Errorf("child award declaration changed")
			}
			return err
		}},
		{"approval", func(tx *storeTx) error {
			q := claimSQL{dialect: s.dialect}
			for _, id := range []string{node, "build"} {
				var eligible bool
				err := tx.QueryRowContext(t.Context(), `SELECT `+evaluationSourceSQL(&q, "n")+` FROM nodes n WHERE team=? AND run_id=? AND node_id=?`, team, run, id).Scan(&eligible)
				if err != nil {
					return err
				}
				if eligible != (id == node) {
					return fmt.Errorf("approval or skip_if declaration changed")
				}
			}
			return nil
		}},
	} {
		t.Run(reader.name, func(t *testing.T) {
			if err := s.inTx(t.Context(), reader.read); err != nil {
				t.Fatal(err)
			}
		})
	}
}

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
