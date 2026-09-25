package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A run that stored events before the counters existed arrives with them
// counted, so the per-run caps hold for it from the first append after the
// upgrade.
func TestSchemaV52BackfillsRunEventCounters(t *testing.T) {
	ctx := context.Background()
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := st.AppendEvent(ctx, "run-1", "", "kind", []byte("123456")); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`ALTER TABLE runs DROP COLUMN event_bytes`,
		`ALTER TABLE runs DROP COLUMN event_count`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 52`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v51 to v52: %v", err)
	}
	defer func() { _ = up.Close() }()
	var bytes, count int64
	if err := up.DB().QueryRow(storetest.Rebind(up,
		`SELECT event_bytes, event_count FROM runs WHERE id = ?`), "run-1").Scan(&bytes, &count); err != nil {
		t.Fatal(err)
	}
	if bytes != 30 || count != 3 {
		t.Fatalf("backfilled counters = %d bytes, %d events; want 30 bytes, 3 events", bytes, count)
	}
}
