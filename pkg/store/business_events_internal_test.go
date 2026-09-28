package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Events written at the same instant read back in one order every time,
// ordered by id, because Postgres returns ties in whatever order it likes.
func TestBusinessEventsReadTiesInIDOrder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0)
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"a", "b", "c", "d", "e"} {
		if err := RecordBusinessEvent(tx, BusinessEvent{At: at, Team: DefaultTeam, Kind: "test.tie", SubjectID: subject}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	events, err := st.defaultTenant().BusinessEvents(ctx, "test.tie")
	if err != nil || len(events) != 5 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	ids := make([]string, len(events))
	for i, ev := range events {
		ids[i] = ev.ID
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("tied events read in %v, want id order", ids)
	}
}
