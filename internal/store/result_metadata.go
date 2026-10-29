package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// StepReport fences step state to the attempt that produced it.
type StepReport struct {
	Attempt     runspec.AttemptIdentity
	Actor       runstate.Actor
	StepID      string
	State       string
	MaskedError string
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// AnnotationWrite carries already masked text; the store rechecks the shared annotation byte limit without truncation.
type AnnotationWrite struct {
	Ref    runspec.NodeRef
	Actor  runstate.Actor
	StepID string
	nodeproto.Annotation
}

// SummaryWrite carries masked job or step markdown under the writer's live binding.
type SummaryWrite struct {
	Ref      runspec.NodeRef
	Actor    runstate.Actor
	StepID   string
	Markdown string
}

func checkResultWriterTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, actor runstate.Actor) (runstate.NodeRow, error) {
	p, err := resolveTeamActorTx(ctx, tx, team, actor, sameTeam)
	if err != nil {
		return runstate.NodeRow{}, err
	}
	if !slices.Contains(p.Capability.Grants, capability.NodeReport) {
		return runstate.NodeRow{}, authorityError(apierr.Forbidden, "job reporting grant is required")
	}
	if err := lockStateRunTx(ctx, tx, team, string(ref.RunID)); err != nil {
		return runstate.NodeRow{}, err
	}
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return row, err
	}

	if err := checkHolderTx(ctx, tx, team, row, actor); err != nil {
		return row, err
	}
	if row.Attempt <= 0 {
		return row, holderRefusal()
	}
	var writer string
	var generation int64
	err = tx.QueryRowContext(ctx, "SELECT capability_prefix,generation FROM node_attempts WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND finished_at IS NULL", team, ref.RunID, ref.NodeID, row.Attempt).Scan(&writer, &generation)
	if err != nil {
		return row, err
	}
	if writer != actor.CapabilityPrefix || runspec.ClaimGeneration(generation) != row.Generation {
		return row, holderRefusal()
	}
	return row, nil
}

func boundedResultText(text string) error {
	if !utf8.ValidString(text) {
		return authorityError(apierr.BadRequest, "result text must be UTF-8")
	}
	if int64(len(text)) > limits.AnnotationBytes {
		return authorityError(apierr.PayloadTooLarge, "result text exceeds the annotation byte limit")
	}
	return nil
}

// RecordStep fences each step report to its live attempt.
func (t *Tenant) RecordStep(ctx context.Context, report StepReport) error {
	if report.Attempt.Team != string(t.team) || report.StepID == "" || !slices.Contains([]string{"running", "passed", "failed", "cancelled", "skipped"}, report.State) {
		return authorityError(apierr.BadRequest, "invalid step report")
	}
	if err := boundedResultText(report.MaskedError); err != nil {
		return err
	}
	return t.s.inTxOnce(ctx, func(tx *storeTx) error {
		row, err := checkResultWriterTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(report.Attempt.RunID), NodeID: runspec.NodeID(report.Attempt.NodeID)}, report.Actor)
		if err != nil {
			return err
		}
		if row.Attempt != runspec.AttemptOrdinal(report.Attempt.Attempt) || row.Generation != runspec.ClaimGeneration(report.Attempt.Generation) {
			return holderRefusal()
		}
		return writeStepTx(ctx, tx, t.team, row, report)
	})
}

func writeStepTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, report StepReport) error {
	var state, message string
	var started sql.NullInt64
	var sourceStarted, sourceFinished sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT status,error,started_at,source_started_at,source_finished_at FROM node_steps WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND step_id = ?", team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, report.StepID).Scan(&state, &message, &started, &sourceStarted, &sourceFinished)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sourceStart, sourceEnd := sourceStarted, sourceFinished
	if report.StartedAt != nil {
		sourceStart = sql.NullInt64{Int64: report.StartedAt.UnixNano(), Valid: true}
	}
	if report.FinishedAt != nil {
		sourceEnd = sql.NullInt64{Int64: report.FinishedAt.UnixNano(), Valid: true}
	}
	// safety: runner clocks are observational; only the store clock controls lifecycle checks.
	start := started
	if !start.Valid {
		start = sql.NullInt64{Int64: tx.now(ctx).UnixNano(), Valid: true}
	}
	end := sql.NullInt64{}
	if report.State != "running" {
		end = sql.NullInt64{Int64: tx.now(ctx).UnixNano(), Valid: true}
	}
	if report.State == "running" && report.FinishedAt != nil {
		return authorityError(apierr.BadRequest, "running step cannot have a finish time")
	}
	if exists && state != "running" {
		if state != report.State || message != report.MaskedError || sourceStarted != sourceStart || sourceFinished != sourceEnd {
			return authorityError(apierr.IllegalTransition, "finished step is immutable")
		}
		return nil
	}
	if exists && sourceStarted != sourceStart {
		return authorityError(apierr.Conflict, "step start time is immutable")
	}
	_, err = tx.execContext(ctx, `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,error,started_at,finished_at,source_started_at,source_finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(team,run_id,node_id,attempt,step_id) DO UPDATE SET status = excluded.status,error = excluded.error,finished_at = excluded.finished_at,source_finished_at = excluded.source_finished_at`, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation, report.StepID, report.State, report.MaskedError, start, end, sourceStart, sourceEnd)
	if err != nil {
		return err
	}
	return nil
}

// WriteAnnotation stores bounded masked text under the current writer binding.
func (t *Tenant) WriteAnnotation(ctx context.Context, annotation AnnotationWrite) error {
	if err := boundedResultText(annotation.Message); err != nil {
		return err
	}
	if !utf8.ValidString(annotation.File) {
		return authorityError(apierr.BadRequest, "annotation file is not valid UTF-8")
	}
	if err := annotation.Validate(); err != nil {
		return authorityError(apierr.BadRequest, err.Error())
	}
	encoded, err := json.Marshal(annotation.Annotation)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > limits.AnnotationBytes {
		return authorityError(apierr.PayloadTooLarge, "annotation exceeds the byte limit")
	}
	return t.s.inTxOnce(ctx, func(tx *storeTx) error {
		row, err := checkMetadataWriterTx(ctx, tx, t.team, annotation.Ref, annotation.Actor, annotation.StepID)
		if err != nil {
			return err
		}
		var count, total int64
		err = tx.QueryRowContext(ctx, `SELECT annotation_count,(SELECT COALESCE(SUM(LENGTH(annotations_json)),0) FROM nodes WHERE team = ? AND run_id = ?) + (SELECT COALESCE(SUM(LENGTH(annotations_json)),0) FROM node_steps WHERE team = ? AND run_id = ?) FROM runs WHERE team = ? AND id = ?`, t.team, row.Ref.RunID, t.team, row.Ref.RunID, t.team, row.Ref.RunID).Scan(&count, &total)
		if err != nil {
			return err
		}
		if count >= int64(limits.MaxAnnotations) || total+int64(len(encoded))+2 > limits.RunAnnotationBytes {
			return authorityError(apierr.LimitExceeded, "run annotation limit exceeded")
		}
		var existing []byte
		query, args := resultTarget(row, t.team, annotation.StepID)
		table, where, _ := strings.Cut(query, " WHERE ")
		if err := tx.QueryRowContext(ctx, "SELECT annotations_json FROM "+query, args...).Scan(&existing); err != nil {
			return err
		}
		var entries []nodeproto.Annotation
		if len(existing) > 0 {
			if err := json.Unmarshal(existing, &entries); err != nil {
				return err
			}
		}
		entries = append(entries, annotation.Annotation)
		body, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, "UPDATE "+table+" SET annotations_json = ? WHERE "+where, append([]any{body}, args...)...); err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, "UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE team = ? AND id = ?", annotation.Message, t.team, row.Ref.RunID); err != nil {
			return err
		}
		return nil
	})
}

// WriteSummary stores bounded masked markdown under the current writer binding.
func (t *Tenant) WriteSummary(ctx context.Context, summary SummaryWrite) error {
	if err := boundedResultText(summary.Markdown); err != nil {
		return err
	}
	return t.s.inTxOnce(ctx, func(tx *storeTx) error {
		row, err := checkMetadataWriterTx(ctx, tx, t.team, summary.Ref, summary.Actor, summary.StepID)
		if err != nil {
			return err
		}
		query, args := resultTarget(row, t.team, summary.StepID)
		table, where, _ := strings.Cut(query, " WHERE ")
		result, err := tx.execContext(ctx, "UPDATE "+table+" SET summary = ? WHERE "+where, append([]any{summary.Markdown}, args...)...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func resultTarget(row runstate.NodeRow, team Team, step string) (string, []any) {
	args := []any{team, row.Ref.RunID, row.Ref.NodeID}
	if step == "" {
		return "nodes WHERE team = ? AND run_id = ? AND node_id = ?", args
	}
	return "node_steps WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND step_id = ?", append(args, row.Attempt, step)
}
