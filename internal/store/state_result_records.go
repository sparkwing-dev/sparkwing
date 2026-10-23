package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type nodeResultRecords interface {
	AttachResultRecords(context.Context, *storeTx, Team, runstate.Actor, runstate.NodeRow, runstate.NodeResult) error
}

func attachNodeResultRecordsTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, change nodeTransition) error {
	result := change.Extra.Result
	if result == nil || result.OutputRecord == nil && result.ManifestRecord == nil || change.Cause == causeMaterialize {
		return nil
	}
	if change.Cause != runstate.CauseReport && change.Cause != runstate.CausePublish {
		return authorityError(apierr.BadRequest, "record attachment requires a reported or published result")
	}
	if tx.store.resultRecords == nil {
		return authorityError(apierr.Unavailable, "transactional result record attachment is not configured")
	}
	if err := tx.store.resultRecords.AttachResultRecords(ctx, tx, team, change.Actor, row, *result); err != nil {
		return err
	}

	if err := prepareStateActorTx(ctx, tx, team, string(row.Ref.RunID), change.Actor); err != nil {
		return err
	}
	if change.Cause == runstate.CauseReport {
		return checkHolderTx(ctx, tx, team, row, change.Actor)
	}
	return nil
}
