package store

import (
	"errors"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestStoreDataDownloadRecordsTheDayRow(t *testing.T) {
	s := authorityStore(t)
	now := time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	got, err := (&Tenant{s: s, team: "a"}).ChargeDownload(t.Context(), DownloadCharge{Bytes: 60, FreeCapBytes: 100})
	if err != nil || got.DayBytes != 60 {
		t.Fatalf("final-schema download charge=%+v: %v", got, err)
	}
	var recorded int64
	if err := s.db.QueryRow("SELECT bytes FROM team_download_day WHERE team='a' AND period='2026-10-15'").Scan(&recorded); err != nil || recorded != 60 {
		t.Fatalf("day usage=%d: %v", recorded, err)
	}
}

func TestStoreDataDownloadCapCrossingFundingAndMidnight(t *testing.T) {
	s := authorityStore(t)
	now := time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	request := DownloadCharge{Bytes: 60, FreeCapBytes: 100, FundedCapBytes: 1000}
	for _, want := range []int64{60, 120} {
		got, err := tenant.ChargeDownload(t.Context(), request)
		if err != nil || got.DayBytes != want || got.CapBytes != 100 || got.Tier != teamTierFree {
			t.Fatalf("charge=%+v, want %d: %v", got, want, err)
		}
	}
	request.Bytes = 0
	got, err := tenant.ChargeDownload(t.Context(), request)
	var refusal *DownloadCapError
	if got != (DownloadCharged{}) || !errors.As(err, &refusal) || !errors.Is(err, ErrDownloadCap) || refusal.UsedBytes != 120 || refusal.CapBytes != 100 || refusal.RetryAfter != 2*time.Hour {
		t.Fatalf("exhausted download=%+v: %v", got, err)
	}
	for _, want := range []string{"120", "100", "support@sparkwing.dev", "ask the operator"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("download refusal %q omits %q", err, want)
		}
	}
	request.Record, request.Bytes = true, 500
	if got, err := tenant.ChargeDownload(t.Context(), request); err != nil || got.DayBytes != 620 {
		t.Fatalf("delivered stream=%+v: %v", got, err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MicroCreditsPerCredit, Reference: "download-fixture", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	request.Record, request.Bytes = false, 200
	if got, err := tenant.ChargeDownload(t.Context(), request); err != nil || got.DayBytes != 820 || got.CapBytes != 1000 || got.Tier != teamTierFunded {
		t.Fatalf("funded charge=%+v: %v", got, err)
	}
	now = now.Add(3 * time.Hour)
	if got, err := tenant.ChargeDownload(t.Context(), request); err != nil || got.DayBytes != 200 {
		t.Fatalf("new day=%+v: %v", got, err)
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", DefaultTeam)
	operator := &Tenant{s: s, team: DefaultTeam}
	request.Bytes = 1 << 40
	if got, err := operator.ChargeDownload(t.Context(), request); err != nil || got.CapBytes != 0 {
		t.Fatalf("operator charge=%+v: %v", got, err)
	}
}

func TestStoreDataDownloadSharesReservationsAndExpiresHolds(t *testing.T) {
	s := authorityStore(t)
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	held, err := tenant.ReserveQuota(t.Context(), "download", 100)
	if err != nil {
		t.Fatal(err)
	}
	request := DownloadCharge{FreeCapBytes: 100}
	if _, err := tenant.ChargeDownload(t.Context(), request); !errors.Is(err, ErrDownloadCap) {
		t.Fatalf("held download capacity admitted: %v", err)
	}
	now = held.ExpiresAt
	request.Bytes = 100
	if _, err := tenant.ChargeDownload(t.Context(), request); err != nil {
		t.Fatalf("expired hold retained capacity: %v", err)
	}
	if err := tenant.SettleQuota(t.Context(), held.ID, 0); err != nil {
		t.Fatal(err)
	}
	var used, reserved int64
	if err := s.db.QueryRow("SELECT used,reserved FROM quota_admission WHERE team='a' AND quota='download'").Scan(&used, &reserved); err != nil || used != 100 || reserved != 0 {
		t.Fatalf("settled quota=(%d,%d): %v", used, reserved, err)
	}
}

func TestStoreDataDownloadReplicasRaceForLastCapacity(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite}
	now := time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)
	s.clock, other.clock = func() time.Time { return now }, func() time.Time { return now }
	tenants := []*Tenant{{s: s, team: "a"}, {s: other, team: "a"}}
	if _, err := tenants[0].ChargeDownload(t.Context(), DownloadCharge{Bytes: 40, FreeCapBytes: 100}); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 8)
	for i := range 8 {
		go func() {
			<-start
			_, err := tenants[i%2].ChargeDownload(t.Context(), DownloadCharge{Bytes: 60, FreeCapBytes: 100})
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range 8 {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, ErrDownloadCap) {
			t.Error(err)
		}
	}
	if winners != 1 {
		t.Fatalf("download race had %d winners", winners)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if _, err := tenants[0].ChargeDownload(t.Context(), DownloadCharge{FreeCapBytes: 100}); !errors.Is(err, ErrDownloadCap) {
		t.Fatalf("restart restored spent capacity: %v", err)
	}
}

func TestStoreDataDownloadTenantIsolationAndConservation(t *testing.T) {
	property := func(first, second, foreign uint16) bool {
		s := authorityStore(t)
		path := blobAccountingDiskStore(t, s)
		other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite}
		now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
		s.clock, other.clock = func() time.Time { return now }, func() time.Time { return now }
		amounts := []int64{int64(first), int64(second), int64(foreign)}
		tenants := []*Tenant{{s: s, team: "a"}, {s: other, team: "a"}, {s: other, team: "b"}}
		var wg sync.WaitGroup
		results := make(chan error, len(tenants))
		for i, tenant := range tenants {
			wg.Go(func() {
				_, err := tenant.ChargeDownload(t.Context(), DownloadCharge{Bytes: amounts[i], Record: true})
				results <- err
			})
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Error(err)
				return false
			}
		}
		for team, want := range map[Team]int64{"a": int64(first) + int64(second), "b": int64(foreign)} {
			var used, reported int64
			if err := s.db.QueryRow("SELECT used FROM quota_admission WHERE team=? AND quota='download'", team).Scan(&used); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT COALESCE(SUM(bytes),0) FROM team_download_day WHERE team=?", team).Scan(&reported); err != nil {
				t.Fatal(err)
			}
			if used != want || reported != want {
				t.Logf("team=%s admission=%d reports=%d want=%d", team, used, reported, want)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(527903))})); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataDownloadFailuresRollbackBothCounters(t *testing.T) {
	for _, mode := range []string{"admission_overflow", "day_overflow", "write_failure", "invalid_request"} {
		t.Run(mode, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			s.clock = func() time.Time { return now }
			tenant := &Tenant{s: s, team: "a"}
			request := DownloadCharge{Bytes: 1, Record: true}
			switch mode {
			case "admission_overflow":
				rewriteSchemaExec(t, s.db, "INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES ('a','download','2026-10-15',?,1)", int64(math.MaxInt64))
			case "day_overflow":
				rewriteSchemaExec(t, s.db, "INSERT INTO team_download_day(team,period,bytes,updated_at) VALUES ('a','2026-10-15',?,1)", int64(math.MaxInt64))
			case "write_failure":
				rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_download BEFORE INSERT ON team_download_day BEGIN SELECT RAISE(ABORT,'unavailable'); END")
			case "invalid_request":
				request.Bytes = -1
			}
			var before int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM quota_admission").Scan(&before); err != nil {
				t.Fatal(err)
			}
			got, err := tenant.ChargeDownload(t.Context(), request)
			if err == nil || !reflect.DeepEqual(got, DownloadCharged{}) {
				t.Fatalf("partial charge=%+v: %v", got, err)
			}
			rewriteSchemaCount(t, s.db, "quota_admission", before)
		})
	}
}
