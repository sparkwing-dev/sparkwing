package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type runCancellationObserver func(context.Context, Team, string) error

type runCancellationKey struct {
	team  Team
	runID string
}

// SetRunCancellationObserver replaces the observer of committed run cancellation.
// Register it before starting workers. A nil observer disables notifications.
func (s *Store) SetRunCancellationObserver(observe func(context.Context, Team, string) error) {
	if observe == nil {
		s.runCancellations.Store(nil)
		return
	}
	observer := runCancellationObserver(observe)
	s.runCancellations.Store(&observer)
}

func (t *storeTx) recordRunCancellation(team Team, runID string) {
	key := runCancellationKey{team, runID}
	if !slices.Contains(t.cancelledRuns, key) {
		t.cancelledRuns = append(t.cancelledRuns, key)
	}
}

func (t *storeTx) notifyRunCancellations() error {
	if len(t.cancelledRuns) == 0 {
		return nil
	}
	observer := t.store.runCancellations.Load()
	if observer == nil {
		return nil
	}
	var result error
	for _, key := range t.cancelledRuns {
		result = errors.Join(result, (*observer)(context.WithoutCancel(t.ctx), key.team, key.runID))
	}
	if result != nil {
		return &runCancellationNotificationError{result}
	}
	return nil
}

type runCancellationNotificationError struct{ cause error }

func (e *runCancellationNotificationError) Error() string {
	return fmt.Sprintf("store: run cancellation committed; notification failed: %v", e.cause)
}

func (e *runCancellationNotificationError) Unwrap() error { return e.cause }

func isRunCancellationNotificationError(err error) bool {
	var committed *runCancellationNotificationError
	return errors.As(err, &committed)
}
