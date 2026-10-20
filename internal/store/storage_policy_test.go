package store

import (
	"math"
	"testing"
	"time"
)

func TestStoreDataStoragePolicyDefaultsValidationAndAtomicity(t *testing.T) {
	s := authorityStore(t)
	o := s.AsOperator()
	ctx := t.Context()
	if seeded, err := o.SeedRunRetention(ctx, 90); err != nil || !seeded {
		t.Fatalf("seed=%t: %v", seeded, err)
	}
	want := StorageSettings{RunRetentionDays: 90}
	if got, err := o.StorageSettings(ctx); err != nil || got != want {
		t.Fatalf("default=%+v: %v", got, err)
	}
	want = StorageSettings{RunRetentionDays: 0, NodeMetricRetentionDays: 7, DatabaseAlarmBytes: 123, DefaultTier: StorageTierPaid}
	if err := o.SetStorageSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if seeded, err := o.SeedRunRetention(ctx, 90); err != nil || seeded {
		t.Fatalf("explicit zero overwritten=%t: %v", seeded, err)
	}
	for _, invalid := range []StorageSettings{{RunRetentionDays: -1}, {NodeMetricRetentionDays: -1}, {DatabaseAlarmBytes: -1}, {DefaultTier: "unknown"}, {RunRetentionDays: math.MaxInt64}} {
		if err := o.SetStorageSettings(ctx, invalid); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	if got, err := o.StorageSettings(ctx); err != nil || got != want {
		t.Fatalf("invalid writes changed settings=%+v: %v", got, err)
	}
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_storage_setting BEFORE UPDATE ON settings WHEN NEW.key='storage_default_tier' BEGIN SELECT RAISE(ABORT,'settings unavailable'); END")
	if err := o.SetStorageSettings(ctx, StorageSettings{RunRetentionDays: 2}); err == nil {
		t.Fatal("partial settings write accepted")
	}
	if got, err := o.StorageSettings(ctx); err != nil || got != want {
		t.Fatalf("rollback=%+v: %v", got, err)
	}
}

func TestStoreDataStoragePolicyCorruptAndSealedValuesFail(t *testing.T) {
	for _, raw := range []string{`null`, `"7"`, `-1`, `1.5`, `garbage`} {
		t.Run(raw, func(t *testing.T) {
			s := authorityStore(t)
			rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)", []byte(raw))
			if _, err := s.AsOperator().StorageSettings(t.Context()); err == nil {
				t.Fatal("corrupt policy accepted")
			}
		})
	}
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value,sealed) VALUES ('storage_default_tier',?,1)", []byte(`"free"`))
	if _, err := s.AsOperator().StorageSettings(t.Context()); err == nil {
		t.Fatal("sealed policy accepted")
	}
	if err := s.AsOperator().SetStorageSettings(t.Context(), StorageSettings{}); err == nil {
		t.Fatal("sealed policy overwritten")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial write count=%d: %v", count, err)
	}
}

func TestStoreDataStoragePolicyDefaultTierAndAllowance(t *testing.T) {
	s := authorityStore(t)
	if err := s.AsOperator().SetStorageSettings(t.Context(), StorageSettings{DefaultTier: StorageTierPaid}); err != nil {
		t.Fatal(err)
	}
	a := &Tenant{s: s, team: "a"}
	q, err := a.StorageQuota(t.Context())
	if err != nil || q.Team != "a" || q.Tier != StorageTierPaid || q.MaxBytesPerMonth != PaidTierQuota.MaxBytesPerMonth {
		t.Fatalf("default quota=%+v: %v", q, err)
	}
	if q, err = a.SetStorageAllowance(t.Context(), 456); err != nil || q.Tier != StorageTierPaid || q.MaxBytesPerMonth != PaidTierQuota.MaxBytesPerMonth {
		t.Fatalf("allowance removed default limits=%+v: %v", q, err)
	}
	if err := a.SetStorageQuota(t.Context(), StorageQuota{}); err != nil {
		t.Fatal(err)
	}
	q, err = a.StorageQuota(t.Context())
	if err != nil || !q.Unlimited() || q.Tier != "" || q.AllowanceBytes != 456 {
		t.Fatalf("explicit unlimited=%+v: %v", q, err)
	}
}

func TestStoreDataStoragePolicyUsesLeasedRetention(t *testing.T) {
	s, _, _, _ := resultFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	finished := now.Add(-10 * 24 * time.Hour)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET status='failed',finished_at=? WHERE team='a' AND id='run'", finished.UnixNano())
	o := s.AsOperator()
	if err := o.SetStorageSettings(t.Context(), StorageSettings{RunRetentionDays: 11}); err != nil {
		t.Fatal(err)
	}
	lease, err := o.AcquireSingletonLease(t.Context(), "maintenance", "storage", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := o.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 10})
	if err != nil || retained.Runs != 0 {
		t.Fatalf("early retention=%+v: %v", retained, err)
	}
	if err := o.SetStorageSettings(t.Context(), StorageSettings{RunRetentionDays: 9}); err != nil {
		t.Fatal(err)
	}
	retained, err = o.MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 10})
	if err != nil || retained.Runs != 1 {
		t.Fatalf("expired retention=%+v: %v", retained, err)
	}
}
