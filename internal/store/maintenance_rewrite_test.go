package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func maintenanceStore(t *testing.T) (*Store, *Tenant, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	s := &Store{db: rewriteSchemaDB(t), clock: func() time.Time { return now }}
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
	return s, &Tenant{s: s, team: "a"}, &now
}

func maintenanceRun(t *testing.T, s *Store, id string, number int, state runstate.RunState) {
	t.Helper()
	var finished any
	if state == runstate.RunSuccess || state == runstate.RunFailed || state == runstate.RunCancelled {
		finished = s.now().UnixNano()
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days)
VALUES ('a',?,?,'pipeline',?,?,'request',1,'gh:1','host','execute',?,'account',1,?,'r30',30)`, id, number, state, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"pipeline","writer":"account/fixture"}}`), []byte(`[]`), finished)
}

func TestRewriteSingletonLeaseFencesExpiredOwners(t *testing.T) {
	s, _, now := maintenanceStore(t)
	ctx := context.Background()
	op := s.AsOperator()
	first, err := op.AcquireSingletonLease(ctx, "maintenance", "one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.AcquireSingletonLease(ctx, "maintenance", "two", time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("live foreign holder: %v", err)
	}
	continued, err := op.AcquireSingletonLease(ctx, "maintenance", "one", time.Minute)
	if err != nil || continued.Generation != first.Generation {
		t.Fatalf("same live holder: %+v %v", continued, err)
	}
	*now = now.Add(time.Minute)
	if _, err := op.RenewSingletonLease(ctx, first, time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired renewal: %v", err)
	}
	second, err := op.AcquireSingletonLease(ctx, "maintenance", "two", time.Minute)
	if err != nil || second.Generation != first.Generation+1 {
		t.Fatalf("new fence: %+v %v", second, err)
	}
	if _, err := op.RenewSingletonLease(ctx, first, time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("stale renewal: %v", err)
	}
	if _, err := op.AcquireSingletonLease(ctx, "unknown", "one", time.Minute); err == nil {
		t.Fatal("accepted unknown singleton")
	}
}

func maintenanceEffects(t *testing.T, s *Store, state runstate.RunState) {
	t.Helper()
	tx, err := s.beginTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	}()
	if err := writeRunEffectsTx(context.Background(), tx, "a", "run", state, s.now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteCompletionOldAcknowledgmentLeavesTerminalDue(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	ctx := context.Background()
	maintenanceRun(t, s, "run", 1, runstate.RunPending)
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "completion", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceEffects(t, s, runstate.RunPending)
	due, err := s.AsOperator().DueRunEffects(ctx, lease, 10)
	if err != nil || len(due) != 1 || due[0].Phase != effectPending {
		t.Fatalf("pending: %+v %v", due, err)
	}
	old := EffectDelivery{RunID: "run", Effect: effectGitHubCheck, Revision: due[0].DesiredRev, Lease: lease}
	maintenanceEffects(t, s, runstate.RunSuccess)
	if err := tenant.AcknowledgeRunEffect(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RetryRunEffect(ctx, old, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	due, err = s.AsOperator().DueRunEffects(ctx, lease, 10)
	if err != nil || len(due) != 4 {
		t.Fatalf("terminal still due: %+v %v", due, err)
	}
	for _, effect := range due {
		if effect.Phase != effectTerminal {
			t.Fatalf("regressed phase: %+v", effect)
		}
		if err := tenant.AcknowledgeRunEffect(ctx, EffectDelivery{RunID: "run", Effect: effect.Effect, Revision: effect.DesiredRev, Lease: lease}); err != nil {
			t.Fatal(err)
		}
	}
	due, err = s.AsOperator().DueRunEffects(ctx, lease, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("delivered: %+v %v", due, err)
	}
	*now = now.Add(time.Minute)
	if err := tenant.AcknowledgeRunEffect(ctx, old); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired acknowledgement: %v", err)
	}
}

func TestRewriteReplicaUsageAndQuotaSettlement(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	ctx := context.Background()
	for _, replica := range []string{"one", "two"} {
		for _, counter := range []string{"egress_bytes", "download_bytes", "storage_bytes", "storage_objects", "runs"} {
			if err := tenant.AddUsage(ctx, UsageDelta{Period: "2027-01", ReplicaID: replica, Counter: counter, Amount: 7}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var total int64
	if err := s.db.QueryRow("SELECT sum(bytes) FROM egress_usage WHERE team = 'a'").Scan(&total); err != nil || total != 14 {
		t.Fatalf("usage = %d: %v", total, err)
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('operator.download_daily_max_bytes',?)", []byte(`100`))
	rewriteSchemaExec(t, s.db, "INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES ('a','download',?,90,1)", now.UTC().Format("2006-01-02"))
	reservation, err := tenant.ReserveQuota(ctx, "download", 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReserveQuota(ctx, "download", 8); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("second reservation: %v", err)
	}
	if err := (&Tenant{s: s, team: "b"}).SettleQuota(ctx, reservation.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign settlement: %v", err)
	}
	if err := tenant.SettleQuota(ctx, reservation.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := tenant.SettleQuota(ctx, reservation.ID, 0); err != nil {
		t.Fatal(err)
	}
	reservation, err = tenant.ReserveQuota(ctx, "download", 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.SettleQuota(ctx, reservation.ID, 6); err != nil {
		t.Fatal(err)
	}
	if err := tenant.SettleQuota(ctx, reservation.ID, 6); err != nil {
		t.Fatal(err)
	}
	if err := tenant.SettleQuota(ctx, reservation.ID, 7); err == nil {
		t.Fatal("conflicting settlement succeeded")
	}
	if err := s.db.QueryRow("SELECT used + reserved FROM quota_admission WHERE team = 'a'").Scan(&total); err != nil || total != 96 {
		t.Fatalf("aggregate = %d: %v", total, err)
	}
	if _, err := tenant.ReserveQuota(ctx, "logs", 1000); err == nil {
		t.Fatal("unknown quota accepted")
	}
}

func TestRewriteReplicasRaceQuotaAndSingletonLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replicas.db")
	stores := make([]*Store, 2)
	now := time.Unix(1_800_000_000, 0)
	for i := range stores {
		db, err := sql.Open("sqlite", sqliteDSN(path))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		})
		stores[i] = &Store{db: db, clock: func() time.Time { return now }}
	}
	schema, err := translateSchema(schemaNeutral, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, stores[0].db, schema)
	rewriteSchemaExec(t, stores[0].db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	rewriteSchemaExec(t, stores[0].db, `INSERT INTO settings(key,value) VALUES ('operator.download_daily_max_bytes',?)`, []byte(`100`))
	rewriteSchemaExec(t, stores[0].db, `INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES ('a','download',?,90,1)`, now.UTC().Format("2006-01-02"))
	for _, kind := range []string{"quota", "singleton"} {
		t.Run(kind, func(t *testing.T) {
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, s := range stores {
				go func() {
					<-start
					var err error
					if kind == "quota" {
						_, err = (&Tenant{s: s, team: "a"}).ReserveQuota(context.Background(), "download", 8)
					} else {
						_, err = s.AsOperator().AcquireSingletonLease(context.Background(), "maintenance", []string{"first", "second"}[i], time.Minute)
					}
					results <- err
				}()
			}
			close(start)
			successes := 0
			for range stores {
				if err := <-results; err == nil {
					successes++
				} else if !errors.Is(err, ErrLockHeld) && !errors.Is(err, ErrStorageQuota) {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatalf("%d winners", successes)
			}
		})
	}
}

func TestRewriteUsageOverflowLeavesCounterUnchanged(t *testing.T) {
	s, tenant, _ := maintenanceStore(t)
	delta := UsageDelta{Period: "period", ReplicaID: "replica", Counter: "egress_bytes", Amount: math.MaxInt64}
	if err := tenant.AddUsage(context.Background(), delta); err != nil {
		t.Fatal(err)
	}
	delta.Amount = 1
	if err := tenant.AddUsage(context.Background(), delta); err == nil {
		t.Fatal("counter overflow succeeded")
	}
	var count int64
	if err := s.db.QueryRow(`SELECT bytes FROM egress_usage WHERE team = 'a'`).Scan(&count); err != nil || count != math.MaxInt64 {
		t.Fatalf("counter = %d: %v", count, err)
	}
}
