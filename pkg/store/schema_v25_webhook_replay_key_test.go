package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestSchemaV25_AddsWebhookReplayKeyConstraintToAnOlderDatabase(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX ` + store.TriggerWebhookReplayKeyIndexName,
		`ALTER TABLE triggers DROP COLUMN webhook_replay_key`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 25`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
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

	if _, err := indexDefinition(t, up, store.TriggerWebhookReplayKeyIndexName); err != nil {
		t.Fatalf("migrated database has no %s index: %v", store.TriggerWebhookReplayKeyIndexName, err)
	}

	ctx := context.Background()
	first := store.Trigger{
		ID: "run-1", Pipeline: "build",
		WebhookDelivery: "delivery-1", WebhookReplayKey: "digest-1", CreatedAt: time.Now(),
	}
	if err := up.CreateTrigger(ctx, first); err != nil {
		t.Fatalf("CreateTrigger after migration: %v", err)
	}
	replay := store.Trigger{
		ID: "run-2", Pipeline: "build",
		WebhookDelivery: "delivery-2", WebhookReplayKey: "digest-1", CreatedAt: time.Now(),
	}
	if err := up.CreateTrigger(ctx, replay); !errors.Is(err, store.ErrDuplicateWebhookDelivery) {
		t.Fatalf("relabeled replay err = %v, want ErrDuplicateWebhookDelivery", err)
	}
}

func TestCreateTrigger_WebhookReplayKeyUniqueness(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	seed := store.Trigger{
		ID: "run-1", Pipeline: "build",
		WebhookDelivery: "delivery-1", WebhookReplayKey: "digest-1", CreatedAt: time.Now(),
	}
	if err := st.CreateTrigger(ctx, seed); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	for _, tc := range []struct {
		name    string
		trigger store.Trigger
		wantErr error
	}{
		{
			"the same body under a fresh delivery id is a replay",
			store.Trigger{ID: "run-2", Pipeline: "build", WebhookDelivery: "delivery-2", WebhookReplayKey: "digest-1"},
			store.ErrDuplicateWebhookDelivery,
		},
		{
			"the same delivery id under a fresh body is a replay too",
			store.Trigger{ID: "run-3", Pipeline: "build", WebhookDelivery: "delivery-1", WebhookReplayKey: "digest-2"},
			store.ErrDuplicateWebhookDelivery,
		},
		{
			"a fresh body under a fresh delivery id is accepted",
			store.Trigger{ID: "run-4", Pipeline: "build", WebhookDelivery: "delivery-3", WebhookReplayKey: "digest-3"},
			nil,
		},
		{
			"submissions without a webhook are exempt",
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
}

func TestFindTriggerByWebhookReplay(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	seed := store.Trigger{
		ID: "run-1", Pipeline: "build",
		WebhookDelivery: "delivery-1", WebhookReplayKey: "digest-1", CreatedAt: time.Now(),
	}
	if err := st.CreateTrigger(ctx, seed); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	for _, tc := range []struct {
		name      string
		replayKey string
		delivery  string
		wantID    string
	}{
		{"resolves by replay key", "digest-1", "delivery-9", "run-1"},
		{"resolves by delivery id", "digest-9", "delivery-1", "run-1"},
		{"reports not found for neither", "digest-9", "delivery-9", ""},
		{"empty arguments never match the default", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.FindTriggerByWebhookReplay(ctx, tc.replayKey, tc.delivery)
			if tc.wantID == "" {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindTriggerByWebhookReplay: %v", err)
			}
			if got.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}
