package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// SegmentCommit fences attribution and completeness to a job attempt or owner unit.
type SegmentCommit struct {
	Subject          blob.LogSubject
	Actor            runstate.Actor
	Segments         []runstate.Segment
	Final            bool
	IncompleteReason string
	RetentionClass   string
}

type logStreamIdentity struct {
	id                                            string
	generation                                    int64
	node, attempt, unit, source, sourceGeneration any
}

func logIdentity(subject blob.LogSubject) (logStreamIdentity, error) {
	identity := logStreamIdentity{}
	if err := subject.Validate(); err != nil {
		return identity, authorityError(apierr.BadRequest, err.Error())
	}
	if subject.Kind == blob.LogJobAttempt {
		identity.node = subject.Attempt.NodeID
		identity.attempt = subject.Attempt.Attempt
		identity.generation = subject.Attempt.Generation
	} else {
		identity.unit = string(subject.Unit.Kind)
		identity.generation = subject.Unit.Generation
		if subject.Unit.Source != nil {
			identity.source = subject.Unit.Source.NodeID
			identity.sourceGeneration = subject.Unit.Source.Generation
		}
	}
	body, err := json.Marshal(subject)
	if err != nil {
		return identity, err
	}
	digest := sha256.Sum256(body)
	identity.id = hex.EncodeToString(digest[:])
	return identity, nil
}

func createLogStreamTx(ctx context.Context, tx *storeTx, subject blob.LogSubject) error {
	identity, err := logIdentity(subject)
	if err != nil {
		return err
	}
	now := tx.now(ctx).UnixNano()
	_, err = tx.execContext(ctx, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,unit_kind,source_node_id,source_generation,prefix,retention_class,created_at,updated_at)
		SELECT team,id,?,?,?,?,?,?,?,?,?,log_retention_class,?,? FROM runs WHERE team=? AND id=?`, identity.id, string(subject.Kind), identity.node, identity.attempt, identity.generation, identity.unit, identity.source, identity.sourceGeneration, identity.id, now, now, subject.Team, subject.RunID)
	return err
}

// RecordSegments atomically commits contiguous segments and seals under the current writer fence.
func (t *Tenant) RecordSegments(ctx context.Context, commit SegmentCommit) error {
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
		key, err := blob.LogObjectKey(commit.Subject, commit.RetentionClass, segment.Index)
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
	if err := lockAuthorityThenRunTx(ctx, tx, t.team, commit.Subject.RunID, commit.Actor); err != nil {
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
		return authorityError(apierr.Conflict, "log stream was not registered with its attempt or unit")
	}
	if err := appendSegmentsTx(ctx, tx, t.team, commit.Subject.RunID, identity, commit.Segments); err != nil {
		return err
	}
	completeness := "open"
	var seal any
	finalDigest = ""
	if commit.Final {
		if err := markLogSlotCommittedTx(ctx, tx, t.team, commit.Subject.RunID, identity.id, 0); err != nil {
			return err
		}
		seal = tx.now(ctx).UnixNano()
		completeness = "complete"
		finalDigest = digest
		if commit.IncompleteReason != "" {
			completeness = "incomplete"
		}
	}
	_, err = tx.execContext(ctx, "UPDATE log_streams SET completeness = ?, incomplete_reason = ?, sealed_at = ?, final_commit_digest = ?, updated_at = ? WHERE team = ? AND run_id = ? AND id = ?", completeness, commit.IncompleteReason, seal, finalDigest, tx.now(ctx).UnixNano(), t.team, commit.Subject.RunID, identity.id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// logSlot is one object a log stream may store; index 0 is its final manifest.
type logSlot struct {
	found, committed bool
	size, created    int64
}

func logSlotTx(ctx context.Context, tx *storeTx, team Team, run, stream string, index int64) (logSlot, error) {
	var slot logSlot
	err := tx.QueryRowContext(ctx, `SELECT size,committed=1,created_at FROM log_slots WHERE team=? AND run_id=? AND stream_id=? AND n=?`+tx.forUpdate(), team, run, stream, index).Scan(&slot.size, &slot.committed, &slot.created)
	if errors.Is(err, sql.ErrNoRows) {
		return logSlot{}, nil
	}
	slot.found = err == nil
	return slot, err
}

// openLogSlotTx admits a new slot as one object of its run and its bytes through the storage rule, and counts them
// retained from now until the object is deleted.
func openLogSlotTx(ctx context.Context, tx *storeTx, team Team, run string, identity logStreamIdentity, index, size int64, key string, committed bool) error {
	if err := admitStorageGrowthTx(ctx, tx, team, storageGrowth{run: run, bytes: size, objects: 1}); err != nil {
		return err
	}
	_, err := tx.execContext(ctx, `INSERT INTO log_slots(team,run_id,stream_id,generation,n,size,committed,object_key,created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		team, run, identity.id, identity.generation, index, size, boolToInt(committed), key, tx.now(ctx).UnixNano())
	return err
}

// commitLogSlotTx marks a segment's slot committed. Its signed size stays counted until Sparkwing deletes the object;
// an object larger than its slot is refused, and a slot nobody signed is admitted now at the verified size.
func commitLogSlotTx(ctx context.Context, tx *storeTx, team Team, run string, identity logStreamIdentity, segment runstate.Segment) error {
	index, verified := segment.Index, segment.StoredBytes
	slot, err := logSlotTx(ctx, tx, team, run, identity.id, index)
	if err != nil {
		return err
	}
	if !slot.found {
		return openLogSlotTx(ctx, tx, team, run, identity, index, verified, segment.ObjectKey, true)
	}
	if verified > slot.size {
		return authorityError(apierr.Conflict, "log object is larger than its signed slot")
	}
	return markLogSlotCommittedTx(ctx, tx, team, run, identity.id, index)
}

func markLogSlotCommittedTx(ctx context.Context, tx *storeTx, team Team, run, stream string, index int64) error {
	_, err := tx.execContext(ctx, `UPDATE log_slots SET committed=1 WHERE team=? AND run_id=? AND stream_id=? AND n=?`, team, run, stream, index)
	return err
}

func appendSegmentsTx(ctx context.Context, tx *storeTx, team Team, run string, identity logStreamIdentity, segments []runstate.Segment) error {
	var next, offset int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(n)+1,1),COALESCE(MAX(end_offset),0) FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ?", team, run, identity.id).Scan(&next, &offset); err != nil {
		return err
	}
	for _, segment := range segments {
		hash, err := hex.DecodeString(segment.Digest)
		if err != nil || len(hash) != sha256.Size || hex.EncodeToString(hash) != segment.Digest || segment.ObjectKey == "" || segment.Index < 1 || segment.Index > int64(limits.MaxLogSegmentNumber) || segment.Offset < 0 || segment.Size <= 0 || segment.StoredBytes <= 0 {
			return authorityError(apierr.BadRequest, "invalid immutable log segment")
		}
		if segment.Size > limits.LogSegmentUploadBytes || segment.StoredBytes > limits.LogSegmentUploadBytes || segment.Offset > limits.AttemptLogBytes-segment.Size {
			return authorityError(apierr.PayloadTooLarge, "log segment exceeds the byte limit")
		}
		if segment.Index < next {
			var existing runstate.Segment
			err := tx.QueryRowContext(ctx, "SELECT n,start_offset,end_offset-start_offset,stored_bytes,sha256,object_key FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ? AND n = ?", team, run, identity.id, segment.Index).Scan(&existing.Index, &existing.Offset, &existing.Size, &existing.StoredBytes, &existing.Digest, &existing.ObjectKey)
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
		_, err = tx.execContext(ctx, `INSERT INTO log_segments(team,run_id,stream_id,generation,n,stored_bytes,sha256,object_key,start_offset,end_offset) VALUES (?,?,?,?,?,?,?,?,?,?)`, team, run, identity.id, identity.generation, segment.Index, segment.StoredBytes, segment.Digest, segment.ObjectKey, segment.Offset, segment.Offset+segment.Size)
		if err != nil {
			return err
		}
		next++
		offset += segment.Size
		if err := commitLogSlotTx(ctx, tx, team, run, identity, segment); err != nil {
			return err
		}
	}
	_, err := tx.execContext(ctx, "UPDATE log_streams SET uploaded_through = ? WHERE team = ? AND run_id = ? AND id = ?", offset, team, run, identity.id)
	return err
}

func checkLogWriterTx(ctx context.Context, tx *storeTx, team Team, subject blob.LogSubject, actor runstate.Actor, sealedReplay bool) error {
	p, err := resolveTeamActorTx(ctx, tx, team, actor, sameTeam)
	if err != nil {
		return err
	}
	c := p.Capability
	if c.Data != nil && c.Data.Use == capability.DataPublish {
		return checkPublishedLogWriterTx(ctx, tx, team, subject, c)
	}
	if c.Prefix != actor.CapabilityPrefix || c.Binding.RunID != subject.RunID || c.Binding.Holder != actor.Holder {
		return holderRefusal()
	}
	if subject.Kind == blob.LogJobAttempt {
		if !slices.Contains(c.Grants, capability.LogTail) || c.Binding.Kind != capability.BindingNode {
			return authorityError(apierr.Forbidden, "job log grant is required")
		}
		a := subject.Attempt
		if a.Team != string(team) || a.RunID != subject.RunID || a.NodeID != c.Binding.NodeID || runspec.ClaimGeneration(a.Generation) != runspec.ClaimGeneration(c.Binding.Generation) || runspec.ClaimGeneration(a.Generation) != actor.Generation || (c.Binding.Attempt > 0 && c.Binding.Attempt != a.Attempt) {
			return holderRefusal()
		}
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(subject.RunID), NodeID: runspec.NodeID(a.NodeID)})
		if err != nil {
			return err
		}
		if row.Generation != runspec.ClaimGeneration(a.Generation) {
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
			if !sealedReplay && tx.now(ctx).UnixNano() > finished.Int64+int64(limits.LogSealGrace) {
				return holderRefusal()
			}
			return nil
		}

		if row.Attempt != runspec.AttemptOrdinal(a.Attempt) {
			return holderRefusal()
		}
		return checkHolderTx(ctx, tx, team, row, actor)
	}
	if subject.Unit == nil {
		return holderRefusal()
	}
	return checkOwnerLogWriterTx(ctx, tx, team, subject, c, actor)
}

func checkOwnerLogWriterTx(ctx context.Context, tx *storeTx, team Team, subject blob.LogSubject, c capability.Capability, actor runstate.Actor) error {
	unit := subject.Unit
	var holder, prefix, status string
	var generation int64
	var lease, finished sql.NullInt64
	if unit.Kind == blob.LogEval {
		source := unit.Source
		if source == nil || c.Unit != capability.UnitEval || c.Binding.Expansion == nil || c.Binding.NodeID != source.NodeID || c.Binding.Generation != source.Generation || c.Binding.Expansion.Generation != unit.Generation || !slices.Contains(c.Grants, capability.ExpansionSubmit) {
			return holderRefusal()
		}
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(claimed_by,''),capability_prefix,status,generation,lease_expires_at,finished_at FROM eval_units WHERE team = ? AND run_id = ? AND source_node_id = ? AND source_generation = ?", team, subject.RunID, source.NodeID, source.Generation).Scan(&holder, &prefix, &status, &generation, &lease, &finished)
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
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(claimed_by,''),capability_prefix,status,generation,lease_expires_at,finished_at FROM triggers WHERE team = ? AND id = ? AND unit_kind = ?", team, subject.RunID, string(unit.Kind)).Scan(&holder, &prefix, &status, &generation, &lease, &finished)
		if err != nil {
			return err
		}
	}
	now := tx.now(ctx)
	if finished.Valid && (status == "done" || status == "failed") && generation == unit.Generation && generation == int64(actor.Generation) &&
		now.UnixNano() >= finished.Int64 && now.UnixNano() < finished.Int64+int64(limits.LogSealGrace) {
		// Retired rows clear the live writer; the immutable awarded credential retains its identity.
		award, err := decodeAwardUnit(c.Unit, c.UnitID)
		if err != nil || (c.Issuance != capability.Award && c.Issuance != capability.Exchange) || c.Prefix != actor.CapabilityPrefix || c.Binding.RunID != subject.RunID || c.Binding.Holder != actor.Holder || string(award.RunID) != subject.RunID {
			return holderRefusal()
		}
		if unit.Source != nil && (string(award.NodeID) != unit.Source.NodeID || award.SourceGeneration != unit.Source.Generation) {
			return holderRefusal()
		}
		return nil
	}
	if (status != "claimed" && status != "running") || holder != actor.Holder || prefix != actor.CapabilityPrefix || runspec.ClaimGeneration(generation) != runspec.ClaimGeneration(unit.Generation) || runspec.ClaimGeneration(generation) != actor.Generation || !lease.Valid || lease.Int64 <= now.UnixNano() {
		return holderRefusal()
	}
	return nil
}

// ReadLogSegments reauthorizes the exact tagged subject and selects its persisted byte ranges.
func (t *Tenant) ReadLogSegments(ctx context.Context, actor runstate.Actor, request blob.LogReadRequest) (runstate.LogSegments, error) {
	result := runstate.LogSegments{Subject: request.Subject}
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
	return inTxResult(ctx, t.s, func(tx *storeTx) (runstate.LogSegments, error) {
		principal, err := resolveTeamActorTx(ctx, tx, t.team, actor, sameTeam)
		if err != nil {
			return result, err
		}
		c := principal.Capability
		if !slices.Contains(c.Grants, capability.RunsRead) && !slices.Contains(c.Grants, capability.LogsRead) && !slices.Contains(c.Grants, capability.LogTail) && !slices.Contains(c.Grants, capability.ExpansionSubmit) {
			return result, authorityError(apierr.Forbidden, "log read grant is required")
		}
		if c.Binding.Kind != capability.BindingNone && c.Binding.RunID != request.Subject.RunID {
			return result, holderRefusal()
		}
		if c.Unit == capability.UnitTrigger {
			owner := actor
			owner.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(c.Binding.RunID), Holder: actor.Holder, Generation: actor.Generation}}

			if err := checkOwnerTx(ctx, tx, t.team, owner, request.Subject.RunID); err != nil {
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
		rows, err := tx.queryContext(ctx, "SELECT n,start_offset,end_offset-start_offset,stored_bytes,sha256,object_key FROM log_segments WHERE team = ? AND run_id = ? AND stream_id = ? AND generation = ? AND end_offset > ? ORDER BY n", t.team, request.Subject.RunID, identity.id, identity.generation, request.Offset)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var segment runstate.Segment
			if err := rows.Scan(&segment.Index, &segment.Offset, &segment.Size, &segment.StoredBytes, &segment.Digest, &segment.ObjectKey); err != nil {
				return result, errors.Join(err, rows.Close())
			}
			result.Segments = append(result.Segments, segment)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return result, err
		}
		return result, nil
	})
}
