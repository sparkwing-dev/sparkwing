package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type logStreamIdentity struct {
	id                                            string
	generation                                    int64
	node, attempt, unit, source, sourceGeneration any
}

func logIdentity(subject blob.LogSubject) (logStreamIdentity, error) {
	identity := logStreamIdentity{}
	if subject.Team == "" || subject.RunID == "" {
		return identity, authorityError(apierr.BadRequest, "log team and run are required")
	}
	switch subject.Kind {
	case blob.LogJobAttempt:
		a := subject.Attempt
		if a == nil || subject.Unit != nil || a.Team != subject.Team || a.RunID != subject.RunID || a.NodeID == "" || a.Attempt <= 0 || a.Generation <= 0 {
			return identity, authorityError(apierr.BadRequest, "invalid job log identity")
		}
		identity.node = subject.Attempt.NodeID
		identity.attempt = subject.Attempt.Attempt
		identity.generation = subject.Attempt.Generation
	case blob.LogOwnerUnit:
		u := subject.Unit
		if subject.Attempt != nil || u == nil || u.Generation <= 0 {
			return identity, authorityError(apierr.BadRequest, "invalid owner log identity")
		}
		switch u.Kind {
		case blob.LogTrigger, blob.LogPlan:
			if u.Source != nil {
				return identity, authorityError(apierr.BadRequest, "trigger and plan logs have no source")
			}
		case blob.LogEval:
			if u.Source == nil || u.Source.NodeID == "" || u.Source.Generation <= 0 {
				return identity, authorityError(apierr.BadRequest, "eval logs require a fenced source")
			}
		default:
			return identity, authorityError(apierr.BadRequest, "unknown owner log unit")
		}
		identity.unit = string(subject.Unit.Kind)
		identity.generation = subject.Unit.Generation
		if subject.Unit.Source != nil {
			identity.source = subject.Unit.Source.NodeID
			identity.sourceGeneration = subject.Unit.Source.Generation
		}
	default:
		return identity, authorityError(apierr.BadRequest, "unknown log identity")
	}
	body, err := json.Marshal(subject)
	if err != nil {
		return identity, err
	}
	digest := sha256.Sum256(body)
	identity.id = hex.EncodeToString(digest[:])
	return identity, nil
}

// LogSegmentObjectKey binds a signing slot to the same subject used by persistence.
// Signers use the run's persisted retention class, never a caller-selected class.
func LogSegmentObjectKey(subject blob.LogSubject, retention string, index int64) (string, error) {
	identity, err := logIdentity(subject)
	if err != nil {
		return "", err
	}
	for _, part := range []string{subject.Team, subject.RunID, retention} {
		if part == "." || part == ".." || strings.ContainsAny(part, "/\\\x00") {
			return "", authorityError(apierr.BadRequest, "invalid log object namespace")
		}
	}
	if retention == "" || index < 0 || index > int64(limits.MaxLogSegmentNumber) {
		return "", authorityError(apierr.BadRequest, "invalid log signing slot")
	}
	return fmt.Sprintf("teams/%s/logs/%s/%s/%s/%d", url.PathEscape(subject.Team), url.PathEscape(retention), url.PathEscape(subject.RunID), identity.id, index), nil
}

func (t *Tenant) recordSegments(ctx context.Context, commit SegmentCommit) error {
	if commit.Subject.Team != string(t.team) {
		return authorityError(apierr.Forbidden, "log subject belongs to another team")
	}
	identity, err := logIdentity(commit.Subject)
	if err != nil {
		return err
	}
	if (!commit.Final && commit.IncompleteReason != "") || !utf8.ValidString(commit.IncompleteReason) {
		return authorityError(apierr.BadRequest, "incomplete reason requires a final log commit")
	}
	for _, segment := range commit.Segments {
		key, err := LogSegmentObjectKey(commit.Subject, commit.RetentionClass, segment.Index)
		if err != nil {
			return err
		}
		if segment.ObjectKey != key {
			return authorityError(apierr.Forbidden, "log segment object belongs to another signing slot")
		}
	}
	body, err := json.Marshal(commit)
	if err != nil {
		return err
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if _, err := resolveActorTx(ctx, tx, commit.Actor); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, t.team, commit.Subject.RunID); err != nil {
		return err
	}
	var retention string
	if err := tx.QueryRowContext(ctx, "SELECT log_retention_class FROM runs WHERE team = ? AND id = ?", t.team, commit.Subject.RunID).Scan(&retention); err != nil {
		return err
	}
	if commit.RetentionClass != retention {
		return authorityError(apierr.Forbidden, "log retention differs from the sealed run")
	}
	var sealed sql.NullInt64
	var finalDigest string
	err = tx.QueryRowContext(ctx, "SELECT sealed_at,final_commit_digest FROM log_streams WHERE team = ? AND run_id = ? AND id = ?", t.team, commit.Subject.RunID, identity.id).Scan(&sealed, &finalDigest)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	replay := sealed.Valid && commit.Final && finalDigest == digest
	if err := checkLogWriterTx(ctx, tx, t.team, commit.Subject, commit.Actor, replay); err != nil {
		return err
	}
	if sealed.Valid {
		if !replay {
			return authorityError(apierr.Conflict, "sealed log stream is immutable")
		}
		return tx.Commit()
	}
	if !exists {
		_, err = tx.ExecContext(ctx, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,unit_kind,source_node_id,source_generation,prefix,retention_class,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.team, commit.Subject.RunID, identity.id, string(commit.Subject.Kind), identity.node, identity.attempt, identity.generation, identity.unit, identity.source, identity.sourceGeneration, identity.id, retention, tx.now().UnixNano(), tx.now().UnixNano())
		if err != nil {
			return err
		}
	}
	if err := appendSegmentsTx(ctx, tx, t.team, commit.Subject.RunID, identity, commit.Segments); err != nil {
		return err
	}
	completeness := "open"
	var seal any
	finalDigest = ""
	if commit.Final {
		seal = tx.now().UnixNano()
		completeness = "complete"
		finalDigest = digest
		if commit.IncompleteReason != "" {
			completeness = "incomplete"
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE log_streams SET completeness = ?, incomplete_reason = ?, sealed_at = ?, final_commit_digest = ?, updated_at = ? WHERE team = ? AND run_id = ? AND id = ?", completeness, commit.IncompleteReason, seal, finalDigest, tx.now().UnixNano(), t.team, commit.Subject.RunID, identity.id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func appendSegmentsTx(ctx context.Context, tx *storeTx, team Team, run string, identity logStreamIdentity, segments []Segment) error {
	var next, offset int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(n)+1,0),COALESCE(MAX(end_offset),0) FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ?", team, run, identity.id).Scan(&next, &offset); err != nil {
		return err
	}
	for _, segment := range segments {
		hash, err := hex.DecodeString(segment.Digest)
		if err != nil || len(hash) != sha256.Size || hex.EncodeToString(hash) != segment.Digest || segment.ObjectKey == "" || segment.Index < 0 || segment.Index > int64(limits.MaxLogSegmentNumber) || segment.Offset < 0 || segment.Size < 0 {
			return authorityError(apierr.BadRequest, "invalid immutable log segment")
		}
		if segment.Size > limits.LogSegmentUploadBytes || segment.Offset > limits.AttemptLogBytes-segment.Size {
			return authorityError(apierr.PayloadTooLarge, "log segment exceeds the byte limit")
		}
		if segment.Index < next {
			var existing Segment
			err := tx.QueryRowContext(ctx, "SELECT n,start_offset,stored_bytes,sha256,object_key FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ? AND n = ?", team, run, identity.id, segment.Index).Scan(&existing.Index, &existing.Offset, &existing.Size, &existing.Digest, &existing.ObjectKey)
			if err != nil {
				return err
			}
			if existing != segment {
				return authorityError(apierr.Conflict, "log segment identity has conflicting content")
			}
			continue
		}
		if segment.Index != next || segment.Offset != offset {
			return authorityError(apierr.Conflict, "log segments must be contiguous")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO log_segments(team,run_id,stream_id,generation,n,stored_bytes,sha256,object_key,start_offset,end_offset) VALUES (?,?,?,?,?,?,?,?,?,?)`, team, run, identity.id, identity.generation, segment.Index, segment.Size, segment.Digest, segment.ObjectKey, segment.Offset, segment.Offset+segment.Size)
		if err != nil {
			return err
		}
		next++
		offset += segment.Size
	}
	_, err := tx.ExecContext(ctx, "UPDATE log_streams SET uploaded_through = ?, stored_bytes = ? WHERE team = ? AND run_id = ? AND id = ?", offset, offset, team, run, identity.id)
	return err
}

func checkLogWriterTx(ctx context.Context, tx *storeTx, team Team, subject blob.LogSubject, actor Actor, sealedReplay bool) error {
	p, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return err
	}
	c := p.Capability
	if c.Team != string(team) || c.Prefix != actor.CapabilityPrefix || c.Binding.RunID != subject.RunID || c.Binding.Holder != actor.Holder {
		return holderRefusal()
	}
	if subject.Kind == blob.LogJobAttempt {
		if !slices.Contains(c.Grants, capability.LogTail) || c.Binding.Kind != capability.BindingNode {
			return authorityError(apierr.Forbidden, "job log grant is required")
		}
		a := subject.Attempt
		if a.Team != string(team) || a.RunID != subject.RunID || a.NodeID != c.Binding.NodeID || a.Generation != c.Binding.Generation || a.Generation != actor.Generation || (c.Binding.Attempt > 0 && c.Binding.Attempt != a.Attempt) {
			return holderRefusal()
		}
		row, err := readNodeTx(ctx, tx, team, NodeRef{subject.RunID, a.NodeID})
		if err != nil {
			return err
		}
		if row.Attempt != a.Attempt || row.Generation != a.Generation {
			return holderRefusal()
		}
		var holder, prefix string
		var generation int64
		var finished sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT holder,capability_prefix,generation,finished_at FROM node_attempts WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ?", team, subject.RunID, a.NodeID, a.Attempt).Scan(&holder, &prefix, &generation, &finished)
		if err != nil {
			return err
		}
		if holder != actor.Holder || prefix != actor.CapabilityPrefix || generation != a.Generation {
			return holderRefusal()
		}
		if finished.Valid {
			if row.Status != StatusDone || (!sealedReplay && tx.now().UnixNano() > finished.Int64+int64(limits.LogSealGrace)) {
				return holderRefusal()
			}
			return nil
		}
		return checkHolderTx(ctx, tx, team, row, actor, tx.now())
	}
	if subject.Unit == nil {
		return holderRefusal()
	}
	return checkOwnerLogWriterTx(ctx, tx, team, subject, c, actor)
}

func checkOwnerLogWriterTx(ctx context.Context, tx *storeTx, team Team, subject blob.LogSubject, c capability.Capability, actor Actor) error {
	unit := subject.Unit
	var holder, prefix, status string
	var generation int64
	var lease sql.NullInt64
	if unit.Kind == blob.LogEval {
		source := unit.Source
		if source == nil || c.Unit != capability.UnitEval || c.Binding.Expansion == nil || c.Binding.NodeID != source.NodeID || c.Binding.Generation != source.Generation || c.Binding.Expansion.Generation != unit.Generation || !slices.Contains(c.Grants, capability.ExpansionSubmit) {
			return holderRefusal()
		}
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(claimed_by,''),capability_prefix,status,generation,lease_expires_at FROM eval_units WHERE team = ? AND run_id = ? AND source_node_id = ? AND source_generation = ?", team, subject.RunID, source.NodeID, source.Generation).Scan(&holder, &prefix, &status, &generation, &lease)
		if err != nil {
			return err
		}
		var sourceGeneration int64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?", team, subject.RunID, source.NodeID).Scan(&sourceGeneration); err != nil {
			return err
		}
		if sourceGeneration != source.Generation {
			return holderRefusal()
		}
	} else {
		if unit.Kind != blob.LogTrigger && unit.Kind != blob.LogPlan {
			return holderRefusal()
		}
		if string(c.Unit) != string(unit.Kind) || c.Binding.Generation != unit.Generation || !slices.Contains(c.Grants, capability.LogTail) {
			return holderRefusal()
		}
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(claimed_by,''),capability_prefix,status,generation,lease_expires_at FROM triggers WHERE team = ? AND id = ? AND unit_kind = ?", team, subject.RunID, string(unit.Kind)).Scan(&holder, &prefix, &status, &generation, &lease)
		if err != nil {
			return err
		}
	}
	if (status != "claimed" && status != "running") || holder != actor.Holder || prefix != actor.CapabilityPrefix || generation != unit.Generation || generation != actor.Generation || !lease.Valid || lease.Int64 <= tx.now().UnixNano() {
		return holderRefusal()
	}
	return nil
}

func (t *Tenant) readLogSegments(ctx context.Context, actor Actor, request blob.LogReadRequest) (LogSegments, error) {
	result := LogSegments{Subject: request.Subject}
	if request.Subject.Team != string(t.team) {
		return result, authorityError(apierr.Forbidden, "log subject belongs to another team")
	}
	if request.Offset < 0 {
		return result, authorityError(apierr.BadRequest, "log offset must be nonnegative")
	}
	identity, err := logIdentity(request.Subject)
	if err != nil {
		return result, err
	}
	// safety: authority rechecks take shared locks, which PostgreSQL forbids in read-only transactions.
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return result, err
	}
	defer rollbackOrLog(tx)
	principal, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return result, err
	}
	c := principal.Capability
	if c.Team != string(t.team) || (!slices.Contains(c.Grants, capability.RunsRead) && !slices.Contains(c.Grants, capability.LogsRead) && !slices.Contains(c.Grants, capability.LogTail) && !slices.Contains(c.Grants, capability.ExpansionSubmit)) {
		return result, authorityError(apierr.Forbidden, "log read grant is required")
	}
	if c.Binding.Kind != capability.BindingNone && c.Binding.RunID != request.Subject.RunID {
		return result, holderRefusal()
	}
	if c.Unit == capability.UnitTrigger {
		owner := actor
		owner.Owner = OwnerLeaseRef{Kind: OwnerTrigger, RunID: c.Binding.RunID, Holder: actor.Holder, Generation: actor.Generation}
		if err := checkOwnerTx(ctx, tx, t.team, owner, request.Subject.RunID, tx.now()); err != nil {
			return result, err
		}
	} else if c.Binding.Kind == capability.BindingNode || c.Unit != "" || (!slices.Contains(c.Grants, capability.RunsRead) && !slices.Contains(c.Grants, capability.LogsRead)) {
		if err := checkLogWriterTx(ctx, tx, t.team, request.Subject, actor, false); err != nil {
			return result, err
		}
	}
	var completeness string
	err = tx.QueryRowContext(ctx, "SELECT completeness,incomplete_reason FROM log_streams WHERE team = ? AND run_id = ? AND id = ? AND generation = ?", t.team, request.Subject.RunID, identity.id, identity.generation).Scan(&completeness, &result.IncompleteReason)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Complete = completeness == "complete"
	rows, err := tx.QueryContext(ctx, "SELECT n,start_offset,stored_bytes,sha256,object_key FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ? AND generation = ? AND end_offset > ? ORDER BY n", t.team, request.Subject.RunID, identity.id, identity.generation, request.Offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var segment Segment
		if err := rows.Scan(&segment.Index, &segment.Offset, &segment.Size, &segment.Digest, &segment.ObjectKey); err != nil {
			return result, errors.Join(err, rows.Close())
		}
		result.Segments = append(result.Segments, segment)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
