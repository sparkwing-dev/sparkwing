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
	exists := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return err
		}
		var previous memoServedReceipt
		if err := json.Unmarshal(raw, &previous); err != nil {
			_ = rows.Close()
			return err
		}
		exists = exists || previous.Attempt == attempt
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if exists {
		return nil
	}
	receipt := memoServedReceipt{Attempt: attempt, Source: original.Key, Digest: original.Digest, Output: hit.Output, Manifest: hit.Manifest}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: auditActor{system: auditController}}, auditMemoServed{RunID: attempt.RunID, Attempt: memoAttemptDigest(attempt), Receipt: memoReceiptDigest(receipt)}); err != nil {
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
			_ = rows.Close()
			return false, err
		}
		var receipt memoServedReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			_ = rows.Close()
			return false, err
		}
		if receipt.Attempt.Team == string(team) && receipt.Attempt.RunID == string(row.Ref.RunID) && receipt.Attempt.NodeID == string(row.Ref.NodeID) && receipt.Attempt.Generation == int64(row.Generation) && receipt.Attempt.Attempt == int64(row.Attempt) {
			receipts = append(receipts, receipt)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, receipt := range receipts {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT attrs FROM audit_events WHERE team=? AND kind=? AND subject_id=? AND run_id=?`, team, runstate.EventMemoServed, memoAttemptDigest(receipt.Attempt).String(), receipt.Attempt.RunID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		var audit auditMemoServed
		if err := json.Unmarshal([]byte(raw), &audit); err != nil {
			return false, err
		}
		if audit.Receipt != memoReceiptDigest(receipt) {
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

type auditMemoServed struct {
	RunID   string      `json:"run_id"`
	Attempt auditDigest `json:"attempt_digest"`
	Receipt auditDigest `json:"receipt_digest"`
}

func (auditMemoServed) isAuditPayload() {}

func memoAttemptDigest(attempt runspec.AttemptIdentity) auditDigest {
	raw, _ := json.Marshal(attempt)
	return digestAudit(string(raw))
}
func memoReceiptDigest(receipt memoServedReceipt) auditDigest {
	raw, _ := json.Marshal(receipt)
	return digestAudit(string(raw))
}
