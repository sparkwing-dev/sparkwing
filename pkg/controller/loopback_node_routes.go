package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type loopbackBounces interface {
	PendingNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error)
	ConsumeNodeBounce(ctx context.Context, runID, nodeID string, seq int64, outcome string) error
}

type loopbackExecutionAttempts interface {
	AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error
	FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error
}

// safety: a backend that cannot hold a bounce request has none pending, so the poll answers
// 204 and a consume answers 404, which a runner reads as already settled.
func (l *Loopback) handlePendingNodeBounce(w http.ResponseWriter, r *http.Request) {
	bounces, ok := l.state.(loopbackBounces)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, err := bounces.PendingNodeBounce(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	if b == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (l *Loopback) handleConsumeNodeBounce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seq     int64  `json:"seq"`
		Outcome string `json:"outcome"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bounces, ok := l.state.(loopbackBounces)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("bounce request %d: %w", body.Seq, store.ErrNotFound))
		return
	}
	if body.Outcome == "" {
		body.Outcome = store.BounceBounced
	}
	if err := bounces.ConsumeNodeBounce(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Seq, body.Outcome); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) executionAttempts(w http.ResponseWriter) (loopbackExecutionAttempts, bool) {
	attempts, ok := l.state.(loopbackExecutionAttempts)
	if !ok {
		writeError(w, http.StatusNotImplemented,
			fmt.Errorf("%w: this run's state backend records no execution attempts", storage.ErrNotSupported))
	}
	return attempts, ok
}

func (l *Loopback) handleAcknowledgeNodeExecutionStart(w http.ResponseWriter, r *http.Request) {
	attempts, ok := l.executionAttempts(w)
	if !ok {
		return
	}
	var body store.ExecutionStart
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.AttemptOrdinal < 1 {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal is required"))
		return
	}
	if err := attempts.AcknowledgeNodeExecutionStart(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body); err != nil {
		writeExecutionAttemptError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleFinishNodeExecutionAttempt(w http.ResponseWriter, r *http.Request) {
	attempts, ok := l.executionAttempts(w)
	if !ok {
		return
	}
	var body store.ExecutionAttemptFinish
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.AttemptOrdinal < 1 || !validExecutionAttemptResult(body.Outcome, body.FailureReason) {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal and a valid outcome are required"))
		return
	}
	if err := attempts.FinishNodeExecutionAttempt(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body); err != nil {
		writeExecutionAttemptError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeExecutionAttemptError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeStateError(w, err)
}
