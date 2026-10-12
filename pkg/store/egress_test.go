package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestEgressUsage_RoundTrip(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 400},
		{Principal: "bob", Month: "2026-09", Bytes: 900},
		{Principal: "alice", Month: "2026-08", Bytes: 10},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}

	rows, err := st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 2 || rows[0].Principal != "bob" || rows[0].Bytes != 900 {
		t.Fatalf("rows = %+v, want bob first at 900", rows)
	}
	if rows[1].Principal != "alice" || rows[1].Bytes != 400 || rows[1].Month != "2026-09" {
		t.Fatalf("second row = %+v, want alice at 400 in 2026-09", rows[1])
	}
	if rows[0].UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}

	all, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage over every month: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("rows over every month = %d, want 3", len(all))
	}
}

func TestEgressUsageOnlyRises(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 900},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}
	// safety: a writer whose memory is behind the row must not hand back
	// budget the row says is already spent.
	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 100},
	}); err != nil {
		t.Fatalf("RecordEgressUsage with a lower total: %v", err)
	}
	rows, err := st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 900 {
		t.Fatalf("rows = %+v, want alice held at 900", rows)
	}

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: "2026-09", Bytes: 1200},
	}); err != nil {
		t.Fatalf("RecordEgressUsage with a higher total: %v", err)
	}
	rows, err = st.ListEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 1200 {
		t.Fatalf("rows = %+v, want alice raised to 1200", rows)
	}
}

func TestEgressUsageSkipsUnnamedRowsAndPrunesOldMonths(t *testing.T) {
	st := storetest.New(t).Open(t)
	ctx := context.Background()

	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "", Month: "2026-09", Bytes: 5},
		{Principal: "alice", Month: "", Bytes: 5},
		{Principal: "alice", Month: "2026-07", Bytes: 5},
		{Principal: "alice", Month: "2026-09", Bytes: 5},
	}); err != nil {
		t.Fatalf("RecordEgressUsage: %v", err)
	}
	if err := st.RecordEgressUsage(ctx, nil); err != nil {
		t.Fatalf("RecordEgressUsage with nothing to write: %v", err)
	}
	all, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("rows = %+v, want only the two fully named ones", all)
	}

	n, err := st.PruneEgressUsage(ctx, "2026-09")
	if err != nil {
		t.Fatalf("PruneEgressUsage: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if zero, err := st.PruneEgressUsage(ctx, ""); err != nil || zero != 0 {
		t.Fatalf("PruneEgressUsage with no month = %d, %v; want 0 and no error", zero, err)
	}
}
