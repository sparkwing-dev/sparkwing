package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DebugBoundary binds an owner-selected pause to persisted execution and sealed policy.
type DebugBoundary struct {
	Ref        store.NodeRef
	Generation int64
	Actor      store.Actor
	Reason     string
	Timeout    time.Duration
}

// DebugBoundaryWaiter idempotently persists and follows each generation/reason under live owner authority.
type DebugBoundaryWaiter interface {
	WaitDebugBoundary(context.Context, DebugBoundary) error
}

func (h *OwnerHost) debugBoundary(ctx context.Context, input RunInput, accepted AcceptedRun, row store.NodeRow, before bool) error {
	boundary, err := debugBoundary(input, accepted, row, before)
	if err != nil || boundary == nil {
		return err
	}
	if h.Debug == nil {
		return errors.New("debug pause requires an authority-bearing boundary adapter")
	}
	return h.Debug.WaitDebugBoundary(ctx, *boundary)
}

func debugBoundary(input RunInput, accepted AcceptedRun, row store.NodeRow, before bool) (*DebugBoundary, error) {
	var spec runspec.Spec
	var job nodeproto.JobSpec
	if json.Unmarshal(accepted.Spec, &spec) != nil {
		return nil, errors.New("debug boundary requires accepted run and job declarations")
	}
	pause := spec.Resolved.Settings.Options.Pause.Value
	if len(pause.Before.Value) == 0 && len(pause.After.Value) == 0 && !pause.OnFailure.Value {
		return nil, nil
	}
	if json.Unmarshal(row.Spec, &job) != nil || row.Ref.RunID != accepted.RunID || row.Ref.NodeID != job.ID {
		return nil, errors.New("debug boundary requires accepted run and job declarations")
	}
	if job.OnFailureOf != "" {
		return nil, nil
	}
	reason := ""
	switch {
	case before && slices.Contains(pause.Before.Value, job.ID):
		reason = store.PauseReasonBefore
	case !before && job.Approval == nil && slices.Contains(pause.After.Value, job.ID):
		reason = store.PauseReasonAfter
	case !before && job.Approval == nil && pause.OnFailure.Value && row.Outcome == store.NodeFailed:
		reason = store.PauseReasonOnFailure
	}
	if reason == "" {
		return nil, nil
	}
	timeout := spec.Resolved.Settings.Options.PauseTimeout.Value
	if timeout <= 0 {
		timeout = defaultDebugPauseTimeout
	}
	return &DebugBoundary{Ref: row.Ref, Generation: row.Generation, Actor: input.Owner, Reason: reason, Timeout: timeout}, nil
}
