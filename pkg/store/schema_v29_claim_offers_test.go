package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV30CompositeRestoresClaimOffersSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v29.db")
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "ready-run", Pipeline: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "ready-run", NodeID: "ready-node", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "ready-run", "ready-node"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE node_claim_offers`,
		`ALTER TABLE nodes DROP COLUMN prefers_labels`,
		`ALTER TABLE nodes DROP COLUMN requested_cores`,
		`ALTER TABLE nodes DROP COLUMN requested_memory_bytes`,
		`ALTER TABLE nodes DROP COLUMN requested_slots`,
		`ALTER TABLE nodes DROP COLUMN offer_started_at`,
		`ALTER TABLE nodes DROP COLUMN offer_priority_target`,
		`ALTER TABLE nodes DROP COLUMN claim_base_priority`,
		`ALTER TABLE nodes DROP COLUMN claim_priority`,
		`ALTER TABLE nodes DROP COLUMN claim_worker_id`,
		`ALTER TABLE nodes DROP COLUMN claim_executor_kind`,
		`ALTER TABLE nodes DROP COLUMN claim_reservation_id`,
		`DROP INDEX idx_executors_executor_id`,
		`ALTER TABLE executors DROP COLUMN executor_id`,
		`DELETE FROM sparkwing_meta WHERE key = 'controller_authority_id'`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 30`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("downgrade with %q: %v", stmt, err)
		}
	}
	deleteFleetRequirements(t, st.DB())
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO executors
    (name, token_prefix, kind, location, capabilities_json, base_priority, priority_ceiling,
     max_concurrent, budget_cores, budget_memory_bytes, principal, last_seen,
     headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth)
VALUES ('legacy-worker', 'swr_legacy', 'agent', 'unknown', '[]', 0, 0, 1, 0, 0, 'legacy', 0, 0, 0, 0, 0)`); err != nil {
		t.Fatalf("seed v28 executor: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("open v29 offer-free shape at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer up.Close()
	var executors int
	if err := up.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM executors`).Scan(&executors); err != nil {
		t.Fatalf("v28 executors table was not preserved: %v", err)
	}
	for table, names := range map[string][]string{
		"executors": {"executor_id"},
		"nodes": {
			"offer_started_at", "offer_priority_target", "prefers_labels",
			"requested_cores", "requested_memory_bytes", "requested_slots",
		},
		"node_claim_offers": {
			"claim_token_prefix", "claim_principal", "holder_id", "run_id", "node_id",
			"executor_name", "membership_id", "reservation_id", "resource_digest", "slot",
			"effective_priority", "offered_at", "last_seen_at", "lease_ns",
		},
	} {
		for _, name := range names {
			var count int
			if err := up.DB().QueryRowContext(ctx, storetest.Rebind(up,
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`), table, name).Scan(&count); err != nil {
				t.Fatalf("inspect %s.%s: %v", table, name, err)
			}
			if count != 1 {
				t.Errorf("migrated schema missing %s.%s", table, name)
			}
		}
	}
	for _, name := range []string{
		"idx_executors_executor_id",
		"idx_node_claim_offers_reservation",
		"idx_node_claim_offers_executor_slot",
		"idx_node_claim_offers_executor_node",
	} {
		var count int
		if err := up.DB().QueryRowContext(ctx, storetest.Rebind(up,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`), name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("migrated schema missing %s", name)
		}
	}
	var executorID, authorityID string
	if err := up.DB().QueryRowContext(ctx, `SELECT executor_id FROM executors WHERE name = 'legacy-worker'`).Scan(&executorID); err != nil {
		t.Fatal(err)
	}
	if err := up.DB().QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = 'controller_authority_id'`).Scan(&authorityID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(executorID, "swex_") || !strings.HasPrefix(authorityID, "swfa_") {
		t.Fatalf("migrated identities executor=%q authority=%q", executorID, authorityID)
	}
	var readyAt, offerStartedAt int64
	if err := up.DB().QueryRowContext(ctx,
		`SELECT ready_at, offer_started_at FROM nodes WHERE run_id = 'ready-run' AND node_id = 'ready-node'`).Scan(&readyAt, &offerStartedAt); err != nil {
		t.Fatal(err)
	}
	if offerStartedAt != readyAt {
		t.Errorf("migrated offer start = %d, ready = %d", offerStartedAt, readyAt)
	}
}
