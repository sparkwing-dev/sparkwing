package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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

// Segment describes one immutable log range; logs use their own signer and never a blob data download.
type Segment struct {
	Index     int64
	Offset    int64
	Size      int64
	Digest    string
	ObjectKey string
}

// SegmentCommit fences attribution and completeness to a job attempt or owner unit.
type SegmentCommit struct {
	Subject          blob.LogSubject
	Actor            Actor
	Segments         []Segment
	Final            bool
	IncompleteReason string
	RetentionClass   string
}

// RecordStep refuses stale attempts instead of letting delayed reports replace a later attempt's steps.
func (t *Tenant) RecordStep(ctx context.Context, report StepReport) error {
	panic("unimplemented: c1-data")
}

// WriteAnnotation refuses an oversized masked annotation so the store cannot split UTF-8 or reveal a raw suffix.
func (t *Tenant) WriteAnnotation(ctx context.Context, annotation AnnotationWrite) error {
	panic("unimplemented: c1-data")
}

// WriteSummary stores job or step markdown only after the claim binding is rechecked.
func (t *Tenant) WriteSummary(ctx context.Context, summary SummaryWrite) error {
	panic("unimplemented: c1-data")
}

// RecordSegments checks subject shape, actor binding, exact persisted generation and
// contiguity. Eval checks its own lease and immutable source generation separately.
// For an unsealed final job attempt, C2 LiveLogTail permits the recorded writer
// until persisted attempt.finished_at + limits.LogSealGrace. The transaction checks
// authority expiry/revocation/membership, exact holder/capability/attempt/generation,
// and that no newer attempt or claim generation exists. This permits the first final
// flush after result completion without reopening execution. After final sealing,
// only the identical final commit replays. Owner units retain their own liveness
// rules; eval does not inherit the job-attempt grace exception.
// The run owns RetentionClass; incomplete logs never change the unit's result.
func (t *Tenant) RecordSegments(ctx context.Context, commit SegmentCommit) error {
	panic("unimplemented: c1-data")
}

// CommitRecord checks all member existence and reference authority before making any digest reachable.
func (t *Tenant) CommitRecord(ctx context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	panic("unimplemented: c1-data")
}

// CopyMemo creates destination output and manifest edges atomically from a server-scoped cache hit.
func (t *Tenant) CopyMemo(ctx context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
	panic("unimplemented: c1-data")
}

// LookupRecord adds no authority merely because a key exists; all returned bytes must be in the caller's read set.
func (t *Tenant) LookupRecord(ctx context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	panic("unimplemented: c1-data")
}

// LookupMemo applies the claim's repo/ref/pipeline scope and the verified default-branch fallback.
func (t *Tenant) LookupMemo(ctx context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
	panic("unimplemented: c1-data")
}

// LookupDepcache follows the single restore-order table and runs cold on absent or invalid bytes.
func (t *Tenant) LookupDepcache(ctx context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
	panic("unimplemented: c1-data")
}

var _ blob.RecordStore = (*Tenant)(nil)

// LogSegments is the persisted read model for one tagged subject, including an empty tail.
type LogSegments struct {
	Subject          blob.LogSubject
	Segments         []Segment
	Complete         bool
	IncompleteReason string
}

// ReadLogSegments selects only the authorized subject's generation and byte range.
// Controller reads reauthorize each request; terminal logs remain readable under
// run-read authority after the writer's lease ends.
func (t *Tenant) ReadLogSegments(ctx context.Context, actor Actor, request blob.LogReadRequest) (LogSegments, error) {
	panic("unimplemented: c1-logs")
}
