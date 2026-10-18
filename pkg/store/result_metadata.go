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
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// StepReport fences step state to the attempt that produced it.
type StepReport struct {
	Attempt     runspec.AttemptIdentity
	Actor       Actor
	StepID      string
	State       string
	MaskedError string
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// AnnotationWrite carries already masked text; the store rechecks the shared annotation byte limit without truncation.
type AnnotationWrite struct {
	Ref     NodeRef
	Actor   Actor
	StepID  string
	Level   string
	Message string
	File    string
	Line    *int
}

// SummaryWrite carries masked job or step markdown under the writer's live binding.
type SummaryWrite struct {
	Ref      NodeRef
	Actor    Actor
	StepID   string
	Markdown string
}

func checkResultWriterTx(ctx context.Context, tx *storeTx, team Team, ref NodeRef, actor Actor) (NodeRow, error) {
	p, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return NodeRow{}, err
	}
	if !slices.Contains(p.Capability.Grants, capability.NodeReport) {
		return NodeRow{}, authorityError(apierr.Forbidden, "job reporting grant is required")
	}
	if err := lockStateRunTx(ctx, tx, team, ref.RunID); err != nil {
		return NodeRow{}, err
	}
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return row, err
	}
	if err := checkHolderTx(ctx, tx, team, row, actor, tx.now()); err != nil {
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
	if writer != actor.CapabilityPrefix || generation != row.Generation {
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
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	row, err := checkResultWriterTx(ctx, tx, t.team, NodeRef{report.Attempt.RunID, report.Attempt.NodeID}, report.Actor)
	if err != nil {
		return err
	}
	if row.Attempt != report.Attempt.Attempt || row.Generation != report.Attempt.Generation {
		return holderRefusal()
	}
	return writeStepTx(ctx, tx, t.team, row, report)
}

func writeStepTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, report StepReport) error {
	var state, message string
	var started, finished sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT status,error,started_at,finished_at FROM node_steps WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND step_id = ?", team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, report.StepID).Scan(&state, &message, &started, &finished)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	start := started
	if report.StartedAt != nil {
		start = sql.NullInt64{Int64: report.StartedAt.UnixNano(), Valid: true}
	}
	if !start.Valid {
		start = sql.NullInt64{Int64: tx.now().UnixNano(), Valid: true}
	}
	end := sql.NullInt64{}
	if report.State != "running" {
		end = sql.NullInt64{Int64: tx.now().UnixNano(), Valid: true}
		if report.FinishedAt != nil {
			end.Int64 = report.FinishedAt.UnixNano()
		}
	}
	if end.Valid && end.Int64 < start.Int64 {
		return authorityError(apierr.BadRequest, "step finish precedes its start")
	}
	if report.State == "running" && report.FinishedAt != nil {
		return authorityError(apierr.BadRequest, "running step cannot have a finish time")
	}
	if exists && state != "running" {
		if report.FinishedAt == nil {
			end = finished
		}
		if state != report.State || message != report.MaskedError || started != start || finished != end {
			return authorityError(apierr.IllegalTransition, "finished step is immutable")
		}
		return tx.Commit()
	}
	if exists && started != start {
		return authorityError(apierr.Conflict, "step start time is immutable")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_steps(team,run_id,node_id,attempt,generation,step_id,status,error,started_at,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(team,run_id,node_id,attempt,step_id) DO UPDATE SET status = excluded.status,error = excluded.error,finished_at = excluded.finished_at`, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation, report.StepID, report.State, report.MaskedError, start, end)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type storedAnnotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    *int   `json:"line,omitempty"`
}

// WriteAnnotation stores bounded masked text under the current writer binding.
func (t *Tenant) WriteAnnotation(ctx context.Context, annotation AnnotationWrite) error {
	if err := boundedResultText(annotation.Message); err != nil {
		return err
	}
	if !utf8.ValidString(annotation.File) || (annotation.Line != nil && *annotation.Line <= 0) {
		return authorityError(apierr.BadRequest, "invalid annotation location")
	}
	entry := storedAnnotation{Level: annotation.Level, Message: annotation.Message, File: annotation.File, Line: annotation.Line}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > limits.AnnotationBytes {
		return authorityError(apierr.PayloadTooLarge, "annotation exceeds the byte limit")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
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
	var entries []storedAnnotation
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &entries); err != nil {
			return err
		}
	}
	entries = append(entries, entry)
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET annotations_json = ? WHERE "+where, append([]any{body}, args...)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET annotation_count = annotation_count + 1, top_annotation = ? WHERE team = ? AND id = ?", annotation.Message, t.team, row.Ref.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteSummary stores bounded masked markdown under the current writer binding.
func (t *Tenant) WriteSummary(ctx context.Context, summary SummaryWrite) error {
	if err := boundedResultText(summary.Markdown); err != nil {
		return err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	row, err := checkMetadataWriterTx(ctx, tx, t.team, summary.Ref, summary.Actor, summary.StepID)
	if err != nil {
		return err
	}
	query, args := resultTarget(row, t.team, summary.StepID)
	table, where, _ := strings.Cut(query, " WHERE ")
	result, err := tx.ExecContext(ctx, "UPDATE "+table+" SET summary = ? WHERE "+where, append([]any{summary.Markdown}, args...)...)
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
	return tx.Commit()
}

func resultTarget(row NodeRow, team Team, step string) (string, []any) {
	args := []any{team, row.Ref.RunID, row.Ref.NodeID}
	if step == "" {
		return "nodes WHERE team = ? AND run_id = ? AND node_id = ?", args
	}
	return "node_steps WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND step_id = ?", append(args, row.Attempt, step)
}
