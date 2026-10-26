package controller

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage(t *testing.T) {
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 10_000_000, Reference: "inventory-funding", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-24 * time.Hour).UnixNano()
	const size = 2 * store.StorageBytesPerGB
	if _, err := teststore.SQL(t, s).ExecContext(t.Context(), "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)", store.DefaultTeam, strings.Repeat("a", 64), size, old); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []store.StorageKind{store.StorageCache, store.StorageLogs} {
		if _, err := teststore.SQL(t, s).ExecContext(t.Context(), "INSERT INTO team_storage(team,store,used_bytes,committed_bytes,storage_charged_through,updated_at) VALUES (?,?,999999,999999,?,?)", store.DefaultTeam, kind, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := tenant.AddUsage(t.Context(), store.UsageDelta{Period: store.StorageMonth(now), Counter: "storage_bytes", Amount: 7}); err != nil {
			t.Fatal(err)
		}
	}
	rate := int64(store.CloudStorageRateMicroPerGBDay)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	server := NewDeployment(s, deploymenttest.Metered(t), slog.New(slog.NewJSONHandler(&logs, nil)))
	server.MaintainStorage(t.Context())
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var record struct{ Level, Msg string }
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record.Level == "ERROR" {
			t.Fatalf("maintenance failed: %s", record.Msg)
		}
	}
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].Kind != store.CreditChargeStorage || charges[0].StorageBytes != size || charges[0].AmountMicro <= 0 {
		t.Fatalf("inventory billing: %+v, %v", charges, err)
	}
	retained, err := tenant.StorageRetainedBytes(t.Context())
	if err != nil || retained != size {
		t.Fatalf("retained=%d, %v", retained, err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "", store.StorageMonth(now))
	if err != nil || usage.MonthBytes != 14 {
		t.Fatalf("recorded usage overwritten: %+v, %v", usage, err)
	}
}
