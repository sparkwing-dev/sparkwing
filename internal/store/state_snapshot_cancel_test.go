package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type snapshotSQLError int

func (e snapshotSQLError) Error() string { return fmt.Sprintf("SQL error %d", e) }
func (e snapshotSQLError) Code() int     { return int(e) }

func TestSnapshotReadCancellationPreservesUnrelatedFailures(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("snapshot wait stopped")
	interrupt, unrelated := snapshotSQLError(9), snapshotSQLError(5)
	for _, err := range []error{interrupt, sql.ErrTxDone} {
		if got := snapshotReadError(ctx, err); !errors.Is(got, err) {
			t.Fatalf("live failure changed: %v to %v", err, got)
		}
	}
	cancel(cause)
	for _, err := range []error{interrupt, sql.ErrTxDone} {
		if got := snapshotReadError(ctx, fmt.Errorf("query: %w", err)); got != cause {
			t.Fatalf("cancelled read = %v, want %v", got, cause)
		}
	}
	for _, err := range []error{nil, unrelated, errors.New("read failed")} {
		if got := snapshotReadError(ctx, err); !errors.Is(got, err) {
			t.Fatalf("unrelated failure changed: %v to %v", err, got)
		}
	}
}

func TestFollowerReadsNormalizeCancelledTransactions(t *testing.T) {
	ref := runspec.NodeRef{RunID: "run", NodeID: "node"}
	authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}
	reads := map[string]func(context.Context, *Tenant) error{
		"snapshot": func(ctx context.Context, tenant *Tenant) error {
			_, err := tenant.ReadSnapshot(ctx, string(ref.RunID))
			return err
		},
		"node": func(ctx context.Context, tenant *Tenant) error {
			_, err := tenant.ReadNode(ctx, ref)
			return err
		},
		"owner snapshot": func(ctx context.Context, tenant *Tenant) error {
			_, err := tenant.ReadOwnerSnapshot(ctx, authority, string(ref.RunID))
			return err
		},
		"owner node": func(ctx context.Context, tenant *Tenant) error {
			_, err := tenant.ReadOwnerNode(ctx, authority, ref)
			return err
		},
	}
	for name, read := range reads {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancelled=%t", name, cancelled), func(t *testing.T) {
				s, connection := singleCursorStore(t)
				tenant := &Tenant{s: s, team: DefaultTeam}
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				cause := errors.New("follower stopped after execution completed")
				injected := false
				connection.beforeQuery = func(query string) error {
					if query == `SELECT COUNT(*) FROM sparkwing_schema_version` || query == databaseTimeSQL(s.dialect) {
						return nil
					}
					injected = true
					if cancelled {
						cancel(cause)
					}
					// database/sql can finish cancellation rollback after the query's context check.
					return sql.ErrTxDone
				}
				want := sql.ErrTxDone
				if cancelled {
					want = cause
				}
				if err := read(ctx, tenant); !injected || err != want {
					t.Fatalf("injected=%t read=%v, want %v", injected, err, want)
				}
			})
		}
	}
}
