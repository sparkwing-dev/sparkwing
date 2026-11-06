package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRunCancellationObserverRunsAfterCommitAndRetriesOnReplay(t *testing.T) {
	s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
	failure := fmt.Errorf("database is locked: %w", errors.Join(&pgconn.PgError{Code: "40001"}, errAuthorityChanged))
	calls := 0
	s.SetRunCancellationObserver(func(ctx context.Context, team Team, runID string) error {
		calls++
		row, err := tenant.ReadRun(ctx, runID)
		if err != nil || team != tenant.Team() || row.CancelRequestedAt == nil || row.StateSeq != 1 {
			t.Fatalf("notification preceded commit: team=%s row=%+v error=%v", team, row, err)
		}
		if calls == 1 {
			return failure
		}
		return nil
	})
	_, err := tenant.TransitionRun(t.Context(), change)
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("committed cancellation was retried or hid notification error: calls=%d error=%v", calls, err)
	}
	busyCalls, waits := 0, 0
	if got := retryOnBusyWithSleep(func() error { busyCalls++; return err }, func(time.Duration) { waits++ }); !errors.Is(got, err) || busyCalls != 1 || waits != 0 {
		t.Fatalf("post-commit notification triggered outer SQL retry: calls=%d waits=%d error=%v", busyCalls, waits, got)
	}
	applied, err := tenant.TransitionRun(t.Context(), change)
	if err != nil || !applied.Replayed || applied.StateSeq != 1 || calls != 2 {
		t.Fatalf("replay did not retry notification: %+v calls=%d error=%v", applied, calls, err)
	}
}

func TestRunCancellationObserverDoesNotSeeRolledBackIntent(t *testing.T) {
	s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
	calls := 0
	s.SetRunCancellationObserver(func(context.Context, Team, string) error { calls++; return nil })
	failure := errors.New("later transaction work failed")
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := transitionRunTx(t.Context(), tx, tenant.Team(), change); err != nil {
			return err
		}
		return failure
	})
	row, readErr := tenant.ReadRun(t.Context(), string(change.RunID))
	if !errors.Is(err, failure) || calls != 0 || readErr != nil || row.CancelRequestedAt != nil {
		t.Fatalf("rolled back intent notified: calls=%d cancellation=%v error=%v read=%v", calls, row.CancelRequestedAt, err, readErr)
	}
}

func TestRunCancellationObserverIncludesInternalTransitionsOncePerCommit(t *testing.T) {
	s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
	change.Actor = runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	calls := 0
	s.SetRunCancellationObserver(func(context.Context, Team, string) error { calls++; return nil })
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		for range 2 {
			if _, err := transitionRunTx(t.Context(), tx, tenant.Team(), change); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("internal cancellation notification: calls=%d error=%v", calls, err)
	}
	s.SetRunCancellationObserver(nil)
	if _, err := tenant.TransitionRun(t.Context(), change); err != nil || calls != 1 {
		t.Fatalf("disabled observer: calls=%d error=%v", calls, err)
	}
}
