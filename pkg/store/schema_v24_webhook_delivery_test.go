package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV24_AddsWebhookDeliveryConstraintToAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhook-delivery.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX ` + store.TriggerWebhookDeliveryIndexName,
		`ALTER TABLE triggers DROP COLUMN webhook_delivery`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 24`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	var indexName string
	if err := up.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		store.TriggerWebhookDeliveryIndexName).Scan(&indexName); err != nil {
		t.Fatalf("migrated database has no %s index: %v", store.TriggerWebhookDeliveryIndexName, err)
	}

	ctx := context.Background()
	first := store.Trigger{ID: "run-1", Pipeline: "build", WebhookDelivery: "delivery-1", CreatedAt: time.Now()}
	if err := up.CreateTrigger(ctx, first); err != nil {
		t.Fatalf("CreateTrigger after migration: %v", err)
	}
	replay := store.Trigger{ID: "run-2", Pipeline: "build", WebhookDelivery: "delivery-1", CreatedAt: time.Now()}
	if err := up.CreateTrigger(ctx, replay); !errors.Is(err, store.ErrDuplicateWebhookDelivery) {
		t.Fatalf("replayed delivery err = %v, want ErrDuplicateWebhookDelivery", err)
	}
}

func TestCreateTrigger_WebhookDeliveryUniqueness(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	seed := store.Trigger{ID: "run-1", Pipeline: "build", WebhookDelivery: "delivery-1", CreatedAt: time.Now()}
	if err := st.CreateTrigger(ctx, seed); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	for _, tc := range []struct {
		name    string
		trigger store.Trigger
		wantErr error
	}{
		{
			"same delivery at another pipeline is a replay",
			store.Trigger{ID: "run-2", Pipeline: "other", WebhookDelivery: "delivery-1"},
			store.ErrDuplicateWebhookDelivery,
		},
		{
			"a fresh delivery is accepted",
			store.Trigger{ID: "run-3", Pipeline: "build", WebhookDelivery: "delivery-2"},
			nil,
		},
		{
			"submissions without a delivery are exempt",
			store.Trigger{ID: "run-4", Pipeline: "build"},
			nil,
		},
		{
			"a second submission without a delivery is exempt too",
			store.Trigger{ID: "run-5", Pipeline: "build"},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.trigger
			tr.CreatedAt = time.Now()
			err := st.CreateTrigger(ctx, tr)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("CreateTrigger: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateTrigger err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	got, err := st.GetTrigger(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if got.WebhookDelivery != "delivery-1" {
		t.Errorf("WebhookDelivery = %q, want delivery-1", got.WebhookDelivery)
	}
}
