package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV27_RewritesTheNULInheritedHolderMarker(t *testing.T) {
	target := storetest.NewSQLite(t)
	ctx := ctxT(t)

	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	acquireT(t, st, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, st, store.AcquireSlotRequest{
		Key: "k", HolderID: "child", RunID: "r1", NodeID: "n",
		InheritedHolderID: "leader", Capacity: 1, Policy: store.OnLimitQueue,
	})
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`UPDATE concurrency_holders SET node_id = ? WHERE key = ? AND holder_id = ?`),
		"\x00inherited:leader", "k", "child"); err != nil {
		t.Fatalf("seed legacy marker: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM sparkwing_schema_version WHERE version >= 27`); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	var nodeID string
	if err := up.DB().QueryRowContext(ctx, storetest.Rebind(up,
		`SELECT node_id FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "child").Scan(&nodeID); err != nil {
		t.Fatalf("read migrated marker: %v", err)
	}
	if want := `\inherited:leader`; nodeID != want {
		t.Fatalf("node_id = %q, want %q", nodeID, want)
	}

	holder, err := up.ActiveConcurrencyHolder(ctx, "k", "child", time.Now())
	if err != nil {
		t.Fatalf("read holder: %v", err)
	}
	if holder.NodeID != "" {
		t.Errorf("migrated inherited holder leaks NodeID %q", holder.NodeID)
	}
}
