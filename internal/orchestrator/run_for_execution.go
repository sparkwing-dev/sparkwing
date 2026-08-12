package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// executionRunGetter is implemented by state backends that distinguish
// a display read of a run from an execution read. Only the controller
// client does: it fetches over HTTP, and GET /api/v1/runs/{id} redacts
// secret-declared args by default.
//
// An optional interface rather than a StateBackend method because
// every other implementation reads a local database directly and has
// nothing to opt into -- widening the interface would make each of
// them carry a pass-through.
type executionRunGetter interface {
	GetRunForExecution(ctx context.Context, runID string) (*store.Run, error)
}

// runForExecution fetches a run whose args the caller is about to
// execute with, unredacted.
//
// Use this, never state.GetRun, anywhere the result feeds reg.Invoke,
// a runner.Request, or the per-run secret masker. A redacted arg would
// have the pipeline run with a literal "***", and seeding the masker
// from redacted args would stop it recognizing the real secret in node
// output -- a worse leak than the one redaction closes.
//
// Falls back to GetRun for local backends, whose reads come straight
// off the database and are never redacted.
func runForExecution(ctx context.Context, state StateBackend, runID string) (*store.Run, error) {
	if ex, ok := state.(executionRunGetter); ok {
		return ex.GetRunForExecution(ctx, runID)
	}
	return state.GetRun(ctx, runID)
}
