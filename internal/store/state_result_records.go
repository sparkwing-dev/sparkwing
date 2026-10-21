package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type nodeResultRecords interface {
	AttachResultRecords(context.Context, *storeTx, Team, Actor, NodeRow, NodeResult) error
}

func attachNodeResultRecordsTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change nodeTransition) error {
	result := change.Extra.Result
	if result == nil || result.OutputRecord == nil && result.ManifestRecord == nil || change.Cause == causeMaterialize {
		return nil
	}
	if change.Cause != CauseReport && change.Cause != CausePublish {
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
	if change.Cause == CauseReport {

		return checkHolderTx(ctx, tx, team, row, change.Actor)
	}
	return nil
}
