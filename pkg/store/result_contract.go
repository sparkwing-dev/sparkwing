package store

import (
	"context"
	"time"

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

// RecordStep refuses stale attempts instead of letting delayed reports replace a later attempt's steps.
func (t *Tenant) RecordStep(ctx context.Context, report StepReport) error {
	return t.recordStep(ctx, report)
}

// WriteAnnotation refuses an oversized masked annotation so the store cannot split UTF-8 or reveal a raw suffix.
func (t *Tenant) WriteAnnotation(ctx context.Context, annotation AnnotationWrite) error {
	return t.writeAnnotation(ctx, annotation)
}

// WriteSummary stores job or step markdown only after the claim binding is rechecked.
func (t *Tenant) WriteSummary(ctx context.Context, summary SummaryWrite) error {
	return t.writeSummary(ctx, summary)
}
