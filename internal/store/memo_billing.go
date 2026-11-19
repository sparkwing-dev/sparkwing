package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type memoServedReceipt struct {
	Attempt  runspec.AttemptIdentity       `json:"attempt"`
	Source   blob.RecordKey                `json:"source"`
	Digest   blob.Digest                   `json:"digest"`
	Output   runspec.Optional[blob.Digest] `json:"output"`
	Manifest runspec.Optional[blob.Digest] `json:"manifest"`
}

func recordMemoServedTx(ctx context.Context, tx *storeTx, team Team, attempt runspec.AttemptIdentity, original blob.Record, hit blob.MemoHit) error {
	if err := lockStateRunTx(ctx, tx, team, attempt.RunID); err != nil {
		return err
	}
	rows, err := tx.queryContext(ctx, `SELECT payload FROM events WHERE team=? AND run_id=? AND node_id=? AND kind=?`, team, attempt.RunID, attempt.NodeID, runstate.EventMemoServed)
	if err != nil {
		return err
	}
	var receipts []memoServedReceipt
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return errors.Join(err, rows.Close())
		}
		var previous memoServedReceipt
		if err := json.Unmarshal(raw, &previous); err != nil {
			continue
		}
		if previous.Attempt == attempt {
			receipts = append(receipts, previous)
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, previous := range receipts {
		authenticated, err := memoReceiptAuthenticatedTx(ctx, tx, team, previous)
		if err != nil {
			return err
		}
		if authenticated {
			return nil
		}
	}
	receipt := memoServedReceipt{Attempt: attempt, Source: original.Key, Digest: original.Digest, Output: hit.Output, Manifest: hit.Manifest}
	audit := auditMemoServed{RunID: attempt.RunID}
	audit.Attempt, err = memoAttemptDigest(attempt)
	if err != nil {
		return err
	}
	audit.Receipt, err = memoReceiptDigest(receipt)
	if err != nil {
		return err
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: auditActor{system: auditController}}, audit); err != nil {
		return err
	}
	_, err = appendEventTx(ctx, tx, team, attempt.RunID, attempt.NodeID, runstate.EventMemoServed, receipt, tx.now(ctx), true)
	return err
}

func memoBillingVerifiedTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow) (bool, error) {
	var steps int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_steps WHERE team=? AND run_id=? AND node_id=? AND generation=? AND attempt=?`, team, row.Ref.RunID, row.Ref.NodeID, row.Generation, row.Attempt).Scan(&steps); err != nil {
		return false, err
	}
	if steps != 0 {
		return false, nil
	}

	rows, err := tx.queryContext(ctx, `SELECT payload FROM events WHERE team=? AND run_id=? AND node_id=? AND kind=? ORDER BY seq DESC`, team, row.Ref.RunID, row.Ref.NodeID, runstate.EventMemoServed)
	if err != nil {
		return false, err
	}
	var receipts []memoServedReceipt
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		var receipt memoServedReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			continue
		}
		if receipt.Attempt.Team == string(team) && receipt.Attempt.RunID == string(row.Ref.RunID) && receipt.Attempt.NodeID == string(row.Ref.NodeID) && receipt.Attempt.Generation == int64(row.Generation) && receipt.Attempt.Attempt == int64(row.Attempt) {
			receipts = append(receipts, receipt)
		}
	}
	if err := rows.Err(); err != nil {
		return false, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, receipt := range receipts {
		authenticated, err := memoReceiptAuthenticatedTx(ctx, tx, team, receipt)
		if err != nil {
			return false, err
		}
		if !authenticated {
			continue
		}
		scope, err := blob.RecordNamespace(receipt.Source)
		if err != nil {
			return false, err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blob_records WHERE team=? AND kind='memo' AND scope=? AND name=? AND digest=?`, team, scope, receipt.Source.Name, receipt.Digest).Scan(&count); err != nil {
			return false, err
		}
		if count != 1 {
			continue
		}
		copied := true
		for _, part := range []struct {
			kind   blob.Kind
			digest runspec.Optional[blob.Digest]
		}{{blob.KindOutput, receipt.Output}, {blob.KindManifest, receipt.Manifest}} {
			if !part.digest.Set {
				continue
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blob_records WHERE team=? AND kind=? AND run_id=? AND node_id=? AND generation=? AND attempt=? AND digest=?`, team, part.kind, row.Ref.RunID, row.Ref.NodeID, row.Generation, row.Attempt, part.digest.Value).Scan(&count); err != nil {
				return false, err
			}
			copied = copied && count == 1
		}
		if copied {
			return true, nil
		}
	}
	return false, nil
}

func memoReceiptAuthenticatedTx(ctx context.Context, tx *storeTx, team Team, receipt memoServedReceipt) (bool, error) {
	attemptDigest, err := memoAttemptDigest(receipt.Attempt)
	if err != nil {
		return false, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT attrs FROM audit_events WHERE team=? AND kind=? AND subject_id=? AND run_id=?`, team, runstate.EventMemoServed, attemptDigest.String(), receipt.Attempt.RunID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var audit auditMemoServed
	if json.Unmarshal([]byte(raw), &audit) != nil {
		return false, nil //nolint:nilerr // safety: malformed audit data cannot authenticate a receipt.
	}
	receiptDigest, err := memoReceiptDigest(receipt)
	if err != nil {
		return false, err
	}
	return audit.Receipt == receiptDigest, nil
}

type auditMemoServed struct {
	RunID   string      `json:"run_id"`
	Attempt auditDigest `json:"attempt_digest"`
	Receipt auditDigest `json:"receipt_digest"`
}

func (auditMemoServed) isAuditPayload() {}

func memoAttemptDigest(attempt runspec.AttemptIdentity) (auditDigest, error) {
	raw, err := json.Marshal(attempt)
	return digestAudit(string(raw)), err
}

func memoReceiptDigest(receipt memoServedReceipt) (auditDigest, error) {
	raw, err := json.Marshal(receipt)
	return digestAudit(string(raw)), err
}
