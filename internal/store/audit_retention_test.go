package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAuditRetentionSQLite(t *testing.T) {
	assertAuditRetention(t, storetest.NewSQLite(t).Open(t))
}

func TestAuditRetentionPostgres(t *testing.T) {
	assertAuditRetention(t, storetest.NewPostgres(t).Open(t))
}

func assertAuditRetention(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := t.Context()
	op := s.AsOperator()
	settings, err := op.StorageSettings(ctx)
	if err != nil || settings.AuditRetentionDays != 400 {
		t.Fatalf("default retention: %+v %v", settings, err)
	}
	now := time.Now().UTC()
	for id, age := range map[string]time.Duration{"old-a": 401 * 24 * time.Hour, "old-b": 402 * 24 * time.Hour, "young": 399 * 24 * time.Hour, "fresh": time.Hour} {
		if _, err := s.DB().Exec(storetest.Rebind(s, `INSERT INTO audit_events(id,ts,team,kind) VALUES (?,?,?,'retention')`), id, now.Add(-age).UnixNano(), "deleted-team"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"old-a", "young"} {
		if _, err := s.DB().Exec(storetest.Rebind(s, `UPDATE audit_events SET actor='changed' WHERE id=?`), id); err == nil {
			t.Fatal("audit update accepted", id)
		}
	}
	for _, query := range []string{`DELETE FROM audit_events WHERE id='young'`, `DELETE FROM audit_events`} {
		if _, err := s.DB().Exec(query); err == nil {
			t.Fatal("young audit deletion accepted", query)
		}
	}
	if n := countWhere(t, s, "audit_events", "kind='retention'"); n != 4 {
		t.Fatalf("mixed delete was not atomic: %d", n)
	}
	lease, err := op.AcquireSingletonLease(ctx, "maintenance", "audit-worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := op.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 1})
		if err != nil || result.AuditEvents != 1 {
			t.Fatalf("bounded audit pruning: %+v %v", result, err)
		}
	}
	result, err := op.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 1})
	if err != nil || result.AuditEvents != 0 {
		t.Fatalf("pruned young events: %+v %v", result, err)
	}
	settings.AuditRetentionDays = 2
	if err := op.SetStorageSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM audit_events WHERE id='young'`); err != nil {
		t.Fatalf("operator horizon not honored: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM audit_events WHERE id='fresh'`); err == nil {
		t.Fatal("new horizon permitted fresh delete")
	}
	for _, days := range []int64{-1, 106752} {
		settings.AuditRetentionDays = days
		if err := op.SetStorageSettings(ctx, settings); err == nil {
			t.Fatal("invalid audit horizon accepted", days)
		}
	}
}
