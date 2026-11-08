package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type snapshotSQLError int

func (e snapshotSQLError) Error() string { return fmt.Sprintf("SQL error %d", e) }
func (e snapshotSQLError) Code() int     { return int(e) }

func TestSnapshotReadCancellationPreservesUnrelatedFailures(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("snapshot wait stopped")
	interrupt, unrelated := snapshotSQLError(9), snapshotSQLError(5)
	if got := snapshotReadError(ctx, interrupt); got != interrupt {
		t.Fatalf("live interrupt = %v", got)
	}
	cancel(cause)
	if got := snapshotReadError(ctx, fmt.Errorf("query: %w", interrupt)); got != cause {
		t.Fatalf("cancelled interrupt = %v", got)
	}
	for _, err := range []error{nil, unrelated, errors.New("read failed")} {
		if got := snapshotReadError(ctx, err); got != err {
			t.Fatalf("unrelated failure changed: %v to %v", err, got)
		}
	}
}
