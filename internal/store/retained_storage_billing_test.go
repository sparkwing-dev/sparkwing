package store

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func retainedBillingFixture(t *testing.T) (*Store, SingletonLease, *time.Time) {
	t.Helper()
	s := authorityStore(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	s.clock = func() time.Time { return now }
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "storage", 60*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rate := int64(CloudStorageRateMicroPerGBDay)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) SELECT name,'fund-'||name,'paid',?,1 FROM teams`, MaxCreditGrantMicro)
	return s, lease, &now
}

func retainedBlob(t *testing.T, s *Store, team, digest string, bytes int64, created time.Time) {
	t.Helper()
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)", team, strings.Repeat(digest, 64), bytes, created.UnixNano())
}

func retainedBill(t *testing.T, s *Store, lease SingletonLease) StorageChargeSweep {
	t.Helper()
	billed, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	return billed
}

func TestStoreDataRetainedStoragePricesUniqueBytesAndTenantAllowance(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	const gb = int64(StorageBytesPerGB)
	free := gb
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageFreeAllowanceBytes: &free}); err != nil {
		t.Fatal(err)
	}
	retainedBlob(t, s, "a", "a", 3*gb, now.Add(-time.Hour))
	retainedBlob(t, s, "b", "a", 4*gb, now.Add(-time.Hour))
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_quotas(team,storage_allowance_bytes,updated_at) VALUES ('a',?,1)", 2*gb)
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,replica_id,bytes,objects,updated_at) VALUES ('a','one','r1',?,1,1),('a','two','r2',?,1,1)", 9*gb, 9*gb)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','fund-a','paid',?,1),('b','fund-b','paid',?,1)`, 2*CloudStorageRateMicroPerGBDay, CloudStorageRateMicroPerGBDay)
	if first := retainedBill(t, s, lease); len(first.Charges) != 0 {
		t.Fatalf("initial pass charged historical bytes: %+v", first)
	}
	*now = now.Add(24 * time.Hour)
	billed := retainedBill(t, s, lease)
	if len(billed.Charges) != 2 || billed.ChargedMicro != 2*CloudStorageRateMicroPerGBDay {
		t.Fatalf("billing=%+v; want b's 3 GB-day charge cut to the one its balance funds", billed)
	}
	for i, want := range []int64{gb, 3 * gb} {
		charge := billed.Charges[i]
		if charge.StorageBytes != want || charge.Seconds != storageChargeDaySeconds || charge.Kind != CreditChargeStorage || charge.RunID != "" || charge.CPUClassCores != 0 || charge.RateMicroPerSecond != 0 {
			t.Fatalf("charge=%+v", charge)
		}
	}
	if replay := retainedBill(t, s, lease); len(replay.Charges) != 0 {
		t.Fatalf("replayed interval=%+v", replay)
	}
	*now = now.Add(24 * time.Hour)
	if unfunded := retainedBill(t, s, lease); len(unfunded.Charges) != 1 || unfunded.Charges[0].Team != "a" {
		t.Fatalf("an empty balance kept accruing storage debt: %+v", unfunded)
	}
	for _, tc := range []struct {
		team Team
		want int64
	}{{"a", 0}, {"b", 0}} {
		balance, err := (&Tenant{s: s, team: tc.team}).CreditBalanceMicro(t.Context())
		if err != nil || balance != tc.want {
			t.Fatalf("team=%s balance=%d: %v", tc.team, balance, err)
		}
	}
}

func TestStoreDataRetainedStorageForgivesUnfundedTimeBeforeTopUp(t *testing.T) {
	for _, balance := range []int64{0, -100} {
		t.Run(fmt.Sprint(balance), func(t *testing.T) {
			s, lease, now := retainedBillingFixture(t)
			rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
			if balance < 0 {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reverses,created_at) VALUES ('a','reversal','reversal',?,'paid-before-clawback',1)`, balance)
			}
			retainedBlob(t, s, "a", "a", StorageBytesPerGB, *now)
			retainedBill(t, s, lease)
			*now = now.Add(24 * time.Hour)
			if billed := retainedBill(t, s, lease); len(billed.Charges) != 0 {
				t.Fatalf("unfunded day created debt: %+v", billed)
			}
			tenant := &Tenant{s: s, team: "a"}
			if got, err := tenant.CreditBalanceMicro(t.Context()); err != nil || got != balance {
				t.Fatalf("unfunded balance = %d, %v; want %d", got, err, balance)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','top-up','paid',?,1)`, CloudStorageRateMicroPerGBDay-balance)
			if replay := retainedBill(t, s, lease); len(replay.Charges) != 0 {
				t.Fatalf("top-up collected forgiven storage: %+v", replay)
			}
			*now = now.Add(24 * time.Hour)
			if billed := retainedBill(t, s, lease); len(billed.Charges) != 1 || billed.ChargedMicro != CloudStorageRateMicroPerGBDay {
				t.Fatalf("funded day = %+v", billed)
			}
			if got, err := tenant.CreditBalanceMicro(t.Context()); err != nil || got != 0 {
				t.Fatalf("funded day balance = %d, %v; want zero", got, err)
			}
		})
	}
}

func TestStoreDataRetainedStorageProRataAndFractionalSeconds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bytes    int64
		duration time.Duration
		want     int64
	}{
		{"fraction", StorageBytesPerGB + StorageBytesPerGB/2, 24 * time.Hour, 499999},
		{"partial-day", 2 * StorageBytesPerGB, 30 * time.Hour, 833332},
		{"subsecond", StorageBytesPerGB, time.Second / 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, lease, now := retainedBillingFixture(t)
			retainedBlob(t, s, "a", "a", tc.bytes, *now)
			retainedBill(t, s, lease)
			*now = now.Add(tc.duration)
			if billed := retainedBill(t, s, lease); billed.ChargedMicro != tc.want {
				t.Fatalf("billed=%+v", billed)
			}
		})
	}
}

func TestStoreDataRetainedStorageLeaseFencesAndRollback(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, *now)
	retainedBill(t, s, lease)
	*now = now.Add(24 * time.Hour)
	bad := lease
	bad.Generation++
	if _, err := s.AsOperator().ChargeRetainedStorage(t.Context(), bad); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("forged lease=%v", err)
	}
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_storage_charge BEFORE INSERT ON credit_charges BEGIN SELECT RAISE(ABORT,'ledger unavailable'); END")
	if sweep, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease); err != nil || len(sweep.Charges) != 0 {
		t.Fatalf("failed tenant charge was retained or stopped the sweep: %+v %v", sweep, err)
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 0)
	var through int64
	if err := s.db.QueryRow("SELECT storage_charged_through FROM team_storage WHERE team='a' AND store='cache'").Scan(&through); err != nil || through != now.Add(-24*time.Hour).UnixNano() {
		t.Fatalf("watermark=%d: %v", through, err)
	}
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_storage_charge")
	if billed := retainedBill(t, s, lease); billed.ChargedMicro != CloudStorageRateMicroPerGBDay {
		t.Fatalf("retry=%+v", billed)
	}
	*now = lease.ExpiresAt
	if _, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired lease=%v", err)
	}
}

func TestStoreDataRetainedStorageConcurrentClientsAndRestart(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, *now)
	retainedBill(t, s, lease)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	*now = now.Add(24 * time.Hour)
	start := make(chan struct{})
	type result struct {
		sweep StorageChargeSweep
		err   error
	}
	results := make(chan result, 2)
	for _, client := range []*Store{s, other} {
		go func() {
			<-start
			sweep, err := client.AsOperator().ChargeRetainedStorage(t.Context(), lease)
			results <- result{sweep, err}
		}()
	}
	close(start)
	var amount int64
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		amount += got.sweep.ChargedMicro
	}
	if amount != CloudStorageRateMicroPerGBDay {
		t.Fatalf("concurrent charges=%d", amount)
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 1)
	if replay := retainedBill(t, other, lease); replay.ChargedMicro != 0 {
		t.Fatalf("restart replay=%+v", replay)
	}
}

func TestStoreDataRetainedStorageOverflowFailsWithoutAdvancing(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", math.MaxInt64, *now)
	retainedBlob(t, s, "a", "b", 1, *now)
	if sweep, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease); err != nil || len(sweep.Charges) != 0 {
		t.Fatalf("overflowing tenant was charged or aborted the sweep: %+v %v", sweep, err)
	}
	rewriteSchemaCount(t, s.db, "team_storage", 0)
	rewriteSchemaCount(t, s.db, "credit_charges", 0)
}

func TestStoreDataRetainedStorageLogsAndBlobsShareOneFreeAllowance(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "billing", 3*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rate, free := int64(StorageBytesPerGB), int64(StorageBytesPerGB/2)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','fund','paid',?,1)`, rate)
	commit := logCommit(actor, attempt)
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	retainedBlob(t, s, "a", "a", StorageBytesPerGB-3, now)
	retainedBill(t, s, lease)
	now = now.Add(24 * time.Hour)
	billed := retainedBill(t, s, lease)
	if len(billed.Charges) != 1 || billed.ChargedMicro != rate/2 || billed.Charges[0].StorageBytes != StorageBytesPerGB/2 {
		t.Fatalf("combined storage=%+v", billed)
	}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil || state.StorageChargedMicro != billed.ChargedMicro || state.BalanceMicro != rate-billed.ChargedMicro {
		t.Fatalf("reported storage=%+v: %v", state, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	rewriteSchemaCount(t, s.db, "log_streams", 0)
	rewriteSchemaCount(t, s.db, "log_segments", 0)
	rewriteSchemaCount(t, s.db, "credit_charges", 1)
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != rate-billed.ChargedMicro {
		t.Fatalf("run deletion restored credit=%d: %v", balance, err)
	}
}

func TestStoreDataRetainedStorageIdleGapAndReleasedBytes(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", 24*StorageBytesPerGB, *now)
	retainedBill(t, s, lease)
	*now = now.Add(time.Hour)
	if billed := retainedBill(t, s, lease); billed.ChargedMicro != CloudStorageRateMicroPerGBDay {
		t.Fatalf("hourly storage=%+v", billed)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blobs WHERE team='a'")
	*now = now.Add(time.Hour)
	if billed := retainedBill(t, s, lease); len(billed.Charges) != 0 {
		t.Fatalf("released bytes billed=%+v", billed)
	}
	*now = now.Add(21 * 24 * time.Hour)
	retainedBlob(t, s, "a", "b", StorageBytesPerGB, now.Add(-time.Hour))
	if billed := retainedBill(t, s, lease); billed.ChargedMicro != CloudStorageRateMicroPerGBDay/24 || billed.Charges[0].Seconds != 3600 {
		t.Fatalf("idle gap billed=%+v", billed)
	}
}

func TestStoreDataRetainedStorageDisabledPriceAndClockRegression(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, *now)
	zero := int64(0)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &zero}); err != nil {
		t.Fatal(err)
	}
	retainedBill(t, s, lease)
	rewriteSchemaCount(t, s.db, "team_storage", 0)
	rate := int64(CloudStorageRateMicroPerGBDay)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	retainedBill(t, s, lease)
	*now = now.Add(-time.Second)
	if billed := retainedBill(t, s, lease); len(billed.Charges) != 0 {
		t.Fatalf("clock regression charged=%+v", billed)
	}
	*now = now.Add(24*time.Hour + time.Second)
	if billed := retainedBill(t, s, lease); billed.ChargedMicro != rate {
		t.Fatalf("clock recovery=%+v", billed)
	}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &zero}); err != nil {
		t.Fatal(err)
	}
	retainedBill(t, s, lease)
	*now = now.Add(24 * time.Hour)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	if billed := retainedBill(t, s, lease); len(billed.Charges) != 0 {
		t.Fatalf("disabled interval billed=%+v", billed)
	}
}

func TestStoreDataRetainedStorageStableInventoryAndMonthBalance(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", 100*StorageBytesPerGB, *now)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000000,1)")
	retainedBill(t, s, lease)
	for range 30 {
		*now = now.Add(24 * time.Hour)
		retainedBlob(t, s, "a", "b", 1, now.Add(-time.Second))
		billed := retainedBill(t, s, lease)
		if billed.ChargedMicro != 100*CloudStorageRateMicroPerGBDay {
			t.Fatalf("active byte set billed=%+v", billed)
		}
		rewriteSchemaExec(t, s.db, "DELETE FROM blobs WHERE team='a' AND digest=?", strings.Repeat("b", 64))
	}
	want := int64(1000000000 - 3000*CloudStorageRateMicroPerGBDay)
	if balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context()); err != nil || balance != want {
		t.Fatalf("month balance=%d: %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 30)
}

func TestStoreDataRetainedStorageCorruptSettingsAndWatermarks(t *testing.T) {
	for _, fault := range []string{"price", "watermark", "inventory"} {
		t.Run(fault, func(t *testing.T) {
			s, lease, now := retainedBillingFixture(t)
			retainedBlob(t, s, "a", "a", StorageBytesPerGB, *now)
			retainedBill(t, s, lease)
			*now = now.Add(24 * time.Hour)
			switch fault {
			case "price":
				rewriteSchemaExec(t, s.db, "UPDATE settings SET value=? WHERE key=?", []byte(`"bad"`), metaKeyStorageRateMicroPerGBDay)
			case "watermark":
				rewriteSchemaExec(t, s.db, "UPDATE team_storage SET storage_charged_through=0 WHERE team='a' AND store='logs'")
			case "inventory":
				rewriteSchemaExec(t, s.db, "UPDATE blobs SET created_at=0 WHERE team='a'")
			}
			sweep, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease)
			if fault == "price" && err == nil || fault != "price" && err != nil || len(sweep.Charges) != 0 {
				t.Fatalf("corrupt %s sweep=%+v, %v", fault, sweep, err)
			}
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		})
	}
}

func TestRetainedStorageSweepIsolatesTeamDamageAndDeletion(t *testing.T) {
	for _, badTeam := range []string{"a", "b"} {
		for _, damage := range []string{"watermarks", "deletion"} {
			t.Run(badTeam+"/"+damage, func(t *testing.T) {
				s, lease, now := retainedBillingFixture(t)
				for _, team := range []string{"a", "b"} {
					retainedBlob(t, s, team, "a", StorageBytesPerGB, *now)
				}
				retainedBill(t, s, lease)
				*now = now.Add(24 * time.Hour)
				if damage == "watermarks" {
					rewriteSchemaExec(t, s.db, "UPDATE team_storage SET storage_charged_through=0 WHERE team=? AND store='logs'", badTeam)
				} else {
					s.SetTestClock(t, func() time.Time { return *now })
					if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), Team(badTeam)); err != nil {
						t.Fatal(err)
					}
				}
				sweep := retainedBill(t, s, lease)
				if len(sweep.Charges) != 1 || sweep.Charges[0].Team == Team(badTeam) || sweep.ChargedMicro != CloudStorageRateMicroPerGBDay {
					t.Fatalf("sweep=%+v", sweep)
				}
			})
		}
	}
}

func TestRetainedStorageSweepReturnsDatabaseFailures(t *testing.T) {
	s, lease, _ := retainedBillingFixture(t)
	rewriteSchemaExec(t, s.db, "DROP TABLE blobs")
	if _, err := s.AsOperator().ChargeRetainedStorage(t.Context(), lease); err == nil {
		t.Fatal("database failure was treated as tenant damage")
	}
}

func TestRetainedStorageBillingUsesDatabaseClock(t *testing.T) {
	s, lease, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, now.Add(-time.Hour))
	recordStoreFences(t, s)
	s.dialect = DialectSQLite
	s.pinnedClock = nil
	s.clock = nil
	// The maintenance lease must stay live when the fixture returns to database time.
	rewriteSchemaExec(t, s.db, `UPDATE leases SET until=? WHERE name='maintenance'`, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	retainedBill(t, s, lease)
	rewriteSchemaExec(t, s.db, `UPDATE team_storage SET storage_charged_through=storage_charged_through-?`, int64(24*time.Hour))
	sweep := retainedBill(t, s, lease)
	if len(sweep.Charges) != 1 || sweep.ChargedMicro < CloudStorageRateMicroPerGBDay {
		t.Fatalf("database-clock billing = %+v", sweep)
	}
}
