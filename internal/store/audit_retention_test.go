package store_test

import (
	"strings"
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
		ageAuditEvents(t, s, "id="+"'"+id+"'", now.Add(-age))
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

// The database owner can disable guards; fixtures use that power to model age
// without allowing application inserts to choose their recording timestamp.
func ageAuditEvents(t *testing.T, s *store.Store, selection string, at time.Time) {
	t.Helper()
	if !strings.Contains(selection, "=") {
		selection = "kind='" + selection + "'"
	}
	if s.Dialect() == store.DialectPostgres {
		if _, err := s.DB().Exec(`ALTER TABLE audit_events DISABLE TRIGGER audit_events_append_only`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := s.DB().Exec(`ALTER TABLE audit_events ENABLE TRIGGER audit_events_append_only`); err != nil {
				t.Fatal(err)
			}
		}()
	} else {
		if _, err := s.DB().Exec(`DROP TRIGGER audit_events_no_UPDATE`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := s.DB().Exec(`CREATE TRIGGER audit_events_no_UPDATE BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END`); err != nil {
				t.Fatal(err)
			}
		}()
	}
	if _, err := s.DB().Exec(storetest.Rebind(s, `UPDATE audit_events SET recorded_at=? WHERE `+selection), at.UnixNano()); err != nil {
		t.Fatal(err)
	}
}

func TestBackdatedOccurrenceCannotExpireAuditSQLite(t *testing.T) {
	assertBackdatedOccurrenceCannotExpireAudit(t, storetest.NewSQLite(t).Open(t))
}

func TestBackdatedOccurrenceCannotExpireAuditPostgres(t *testing.T) {
	assertBackdatedOccurrenceCannotExpireAudit(t, storetest.NewPostgres(t).Open(t))
}

func assertBackdatedOccurrenceCannotExpireAudit(t *testing.T, s *store.Store) {
	t.Helper()
	now := time.Now()
	old := now.Add(-401 * 24 * time.Hour)
	signed, err := s.AsOperator().ResolveSignIn(t.Context(), googleProfile("backdated", "person@example.test"), store.SignUpConditions{}, old)
	if err != nil {
		t.Fatal(err)
	}
	var occurred, recorded int64
	if err := s.DB().QueryRow(storetest.Rebind(s, `SELECT ts,recorded_at FROM audit_events WHERE account=? AND kind='account.admitted'`), signed.Account.ID).Scan(&occurred, &recorded); err != nil {
		t.Fatal(err)
	}
	if occurred != old.UnixNano() || recorded < now.UnixNano() {
		t.Fatalf("occurrence=%d recording=%d", occurred, recorded)
	}
	if _, err := s.DB().Exec(`DELETE FROM audit_events`); err == nil {
		t.Fatal("backdated caller time expired a newly recorded audit row")
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "backdate", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainRetention(t.Context(), store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.AuditEvents != 0 {
		t.Fatalf("pruned backdated new audit: %+v %v", result, err)
	}
	for _, query := range []string{
		`INSERT INTO audit_events(id,ts,kind,recorded_at) VALUES ('forged',1,'probe',1)`,
		`INSERT INTO audit_events(id,ts,kind,recorded_at) VALUES ('forged',1,'probe',0)`,
	} {
		if _, err := s.DB().Exec(query); err == nil {
			t.Fatalf("explicit recording time accepted: %s", query)
		}
	}
}
