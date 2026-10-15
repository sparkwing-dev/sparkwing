package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func freeTeam(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tenant := teamHandle(t, st, team)
	if err := st.GrantFreeSlot(context.Background(), team, time.Now()); err != nil {
		t.Fatalf("%s's free slot: %v", team, err)
	}
	return tenant
}

func reserve(st *store.Store, team store.Team, kind store.StorageKind, n int64, now time.Time) (store.StorageReservation, error) {
	return st.ReserveStorage(context.Background(), store.StorageReserve{
		Team: team, Kind: kind, Bytes: n, TTL: time.Hour, Now: now,
	})
}

type legacyStorageUsage struct{ UsedBytes, ReservedBytes int64 }

func usageOf(t *testing.T, st *store.Store, team store.Team, kind store.StorageKind) legacyStorageUsage {
	t.Helper()
	var out legacyStorageUsage
	err := st.DB().QueryRowContext(t.Context(), storetest.Rebind(st, "SELECT used_bytes,reserved_bytes FROM team_storage WHERE team=? AND store=?"), team, kind).Scan(&out.UsedBytes, &out.ReservedBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("storage of %s: %v", team, err)
	}
	return out
}

// A free team's cache share is three quarters of its allowance and its log
// share three sixteenths, each counted apart: filling one leaves the other
// untouched.
func TestReserveHoldsAFreeTeamToEachShareApart(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	now := time.Now()

	res, err := reserve(st, "team-a", store.StorageCache, 12<<10, now)
	if err != nil {
		t.Fatalf("reserve the whole cache share: %v", err)
	}
	if res.Tier != store.TeamTierFree || res.Granted != 12<<10 || res.ID == "" {
		t.Fatalf("reservation = %+v, want a free grant of 12 KiB with an id", res)
	}
	_, err = reserve(st, "team-a", store.StorageCache, 1, now)
	var quota *store.StorageQuotaError
	if !errors.As(err, &quota) || quota.Used != 12<<10 || quota.Allowed != 12<<10 || quota.Requested != 1 {
		t.Fatalf("one byte past the cache share = %v, want a quota error naming 12 KiB used of 12 KiB", err)
	}
	if _, err := reserve(st, "team-a", store.StorageLogs, 3<<10, now); err != nil {
		t.Fatalf("the log share with the cache share full: %v", err)
	}
	if past, err := reserve(st, "team-a", store.StorageLogs, 1, now); err != nil || past.Granted != 1 {
		t.Fatalf("one byte past the log share = %+v, %v; want it granted without a log-share refusal", past, err)
	}

	if err := st.CommitStorage(context.Background(), store.StorageCommit{ID: res.ID, Team: "team-a", Kind: store.StorageCache, Bytes: 10 << 10, Now: now}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.UsedBytes != 10<<10 || got.ReservedBytes != 0 {
		t.Fatalf("after commit = %+v, want 10 KiB used and nothing reserved", got)
	}
	if _, err := reserve(st, "team-a", store.StorageCache, 2<<10, now); err != nil {
		t.Fatalf("the 2 KiB the commit left: %v", err)
	}
	if _, err := reserve(st, "team-a", store.StorageCache, 1, now); !errors.As(err, &quota) {
		t.Fatalf("past the room the commit left = %v, want a quota error", err)
	}
}

func TestReleaseGivesTheRoomBack(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	now := time.Now()
	res, err := reserve(st, "team-a", store.StorageCache, 12<<10, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseStorage(context.Background(), "team-a", res.ID, now); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.UsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatalf("after release = %+v, want nothing held", got)
	}
	if _, err := reserve(st, "team-a", store.StorageCache, 12<<10, now); err != nil {
		t.Fatalf("the share after a release: %v", err)
	}
	if err := st.ReleaseStorage(context.Background(), "team-a", res.ID, now); err != nil {
		t.Fatalf("a second release of the same reservation: %v", err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.ReservedBytes != 12<<10 {
		t.Fatalf("a repeated release = %+v, want the newer reservation still held", got)
	}
}

func TestReserveUpToGrantsTheRoomLeft(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	now := time.Now()
	if _, err := reserve(st, "team-a", store.StorageCache, 8<<10, now); err != nil {
		t.Fatal(err)
	}
	res, err := st.ReserveStorage(context.Background(), store.StorageReserve{
		Team: "team-a", Kind: store.StorageCache, Bytes: 100 << 10, UpTo: true, TTL: time.Hour, Now: now,
	})
	if err != nil || res.Granted != 4<<10 {
		t.Fatalf("up to 100 KiB with 4 KiB left = %+v, %v; want 4 KiB granted", res, err)
	}
	_, err = st.ReserveStorage(context.Background(), store.StorageReserve{
		Team: "team-a", Kind: store.StorageCache, Bytes: 1 << 10, UpTo: true, TTL: time.Hour, Now: now,
	})
	var quota *store.StorageQuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("up to 1 KiB with no room = %v, want a quota error", err)
	}
}

func TestReserveTiers(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	if err := st.SetFreeTeamSlots(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	freeTeam(t, st, "team-a")
	funded := teamHandle(t, st, "team-f")
	fund(t, funded)
	teamHandle(t, st, "team-n")
	now := time.Now()

	res, err := reserve(st, "team-f", store.StorageCache, 1<<30, now)
	if err != nil || res.Tier != store.TeamTierFunded {
		t.Fatalf("a funded team past any share = %+v, %v; want a funded grant", res, err)
	}
	if _, err := reserve(st, "team-n", store.StorageCache, 1, now); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("a team with neither credits nor a slot = %v, want free storage paused", err)
	}
	res, err = reserve(st, store.DefaultTeam, store.StorageCache, 1<<40, now)
	if err != nil || res.Tier != store.TeamTierFunded {
		t.Fatalf("the operator's team = %+v, %v; want funded", res, err)
	}
}

func race(t *testing.T, writers int, attempt func() error, refused func(error) bool) int {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = attempt()
		}()
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case refused(err):
		default:
			t.Fatalf("a racing attempt failed outright: %v", err)
		}
	}
	return won
}

// safety: a warm pool lets racing attempts overlap rather than queue behind new
// connections, which serialized them and hid the race.
func warmPool(t *testing.T, st *store.Store, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.CreditSettings(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func isQuota(err error) bool {
	var quota *store.StorageQuotaError
	return errors.As(err, &quota)
}

// Writers racing for the last bytes of a share each see room when they look
// alone; the row lock admits exactly one.
func TestReserveRaceForTheLastBytesAdmitsExactlyOne(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	now := time.Now()
	const writers, rounds = 5, 6
	for round := range rounds {
		team := store.Team(fmt.Sprintf("race-%d", round))
		freeTeam(t, st, team)
		if _, err := reserve(st, team, store.StorageCache, 8<<10, now); err != nil {
			t.Fatal(err)
		}
		warmPool(t, st, writers)
		won := race(t, writers, func() error {
			_, err := reserve(st, team, store.StorageCache, 4<<10, now)
			return err
		}, isQuota)
		if won != 1 {
			t.Fatalf("round %d: %d of %d racing reserves took the last 4 KiB, want exactly 1", round, won, writers)
		}
		if got := usageOf(t, st, team, store.StorageCache); got.ReservedBytes != 12<<10 {
			t.Fatalf("round %d: reserved after the race = %d, want the whole 12 KiB share", round, got.ReservedBytes)
		}
	}
	freeTeam(t, st, "alone")
	if _, err := reserve(st, "alone", store.StorageCache, 8<<10, now); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(st, "alone", store.StorageCache, 4<<10, now); err != nil {
		t.Fatalf("the last 4 KiB with no rival: %v", err)
	}
}

// An overwrite that shrinks an object commits a negative size, and the count
// never falls below zero.
func TestCommitOfAShrinkingOverwriteNeverGoesNegative(t *testing.T) {
	st := storetest.Open(t)
	freeTeam(t, st, "team-a")
	now := time.Now()
	res, err := reserve(st, "team-a", store.StorageCache, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitStorage(context.Background(), store.StorageCommit{ID: res.ID, Team: "team-a", Kind: store.StorageCache, Bytes: -50, Now: now}); err != nil {
		t.Fatal(err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.UsedBytes != 0 {
		t.Fatalf("used after a shrinking overwrite = %d, want 0", got.UsedBytes)
	}
}

// A commit is scoped to the team that names it: another team's reservation
// id drops nothing of that team's and counts only for the committer.
func TestCommitNamingAnotherTeamsReservationTouchesOnlyTheCommitter(t *testing.T) {
	st := storetest.Open(t)
	freeTeam(t, st, "team-a")
	freeTeam(t, st, "team-b")
	now := time.Now()
	res, err := reserve(st, "team-a", store.StorageCache, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitStorage(context.Background(), store.StorageCommit{ID: res.ID, Team: "team-b", Kind: store.StorageCache, Bytes: 10, Now: now}); err != nil {
		t.Fatal(err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.ReservedBytes != 10 || got.UsedBytes != 0 {
		t.Fatalf("team-a after team-b named its reservation = %+v, want its reservation untouched", got)
	}
	if got := usageOf(t, st, "team-b", store.StorageCache); got.UsedBytes != 10 {
		t.Fatalf("team-b = %+v, want its own 10 bytes counted", got)
	}
	if err := st.ReleaseStorage(context.Background(), "team-b", res.ID, now); err != nil {
		t.Fatal(err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.ReservedBytes != 10 {
		t.Fatalf("team-a after team-b released its reservation = %+v, want it still held", got)
	}
}

func TestCommitReceiptUsesTeamInItsKey(t *testing.T) {
	st := storetest.Open(t)
	freeTeam(t, st, "team-a")
	freeTeam(t, st, "team-b")
	now := time.Now()
	res, err := reserve(st, "team-a", store.StorageCache, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range []store.Team{"team-b", "team-a"} {
		c := store.StorageCommit{ID: res.ID, Team: team, Kind: store.StorageCache, Bytes: 10, Now: now}
		for range 2 {
			if err := st.CommitStorage(t.Context(), c); err != nil {
				t.Fatal(err)
			}
		}
		if got := usageOf(t, st, team, store.StorageCache); got.UsedBytes != 10 {
			t.Fatalf("%s used %d, want one 10-byte commit", team, got.UsedBytes)
		}
	}
	if got := usageOf(t, st, "team-a", store.StorageCache).ReservedBytes; got != 0 {
		t.Fatalf("owner's reservation still holds %d bytes", got)
	}
}

func TestCommitReceiptCountsReplayOnceAndRejectsChangedBytes(t *testing.T) {
	st := storetest.Open(t)
	freeTeam(t, st, "team-a")
	now := time.Now()
	res, err := reserve(st, "team-a", store.StorageLogs, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	c := store.StorageCommit{ID: res.ID, Team: "team-a", Kind: store.StorageLogs, Bytes: 100, Now: now}
	if err := st.CommitStorage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitStorage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := usageOf(t, st, "team-a", store.StorageLogs); got.UsedBytes != 100 || got.ReservedBytes != 0 {
		t.Fatalf("replayed commit = %+v, want one 100-byte write", got)
	}
	c.Bytes = 99
	if err := st.CommitStorage(t.Context(), c); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("changed replay = %v, want invalid input", err)
	}
}

func charge(st *store.Store, team store.Team, n int64, now time.Time) (store.DownloadCharged, error) {
	return st.ChargeDownload(context.Background(), store.DownloadCharge{
		Team: team, Bytes: n, Now: now, FreeCapBytes: 100, FundedCapBytes: 1000,
	})
}

func TestChargeDownloadHoldsATeamToItsDailyCap(t *testing.T) {
	st := storetest.Open(t)
	freeTeam(t, st, "team-a")
	funded := teamHandle(t, st, "team-f")
	fund(t, funded)
	now := time.Date(2026, 9, 23, 22, 0, 0, 0, time.UTC)

	if got, err := charge(st, "team-a", 60, now); err != nil || got.DayBytes != 60 || got.CapBytes != 100 {
		t.Fatalf("60 of 100 = %+v, %v", got, err)
	}
	if got, err := charge(st, "team-a", 60, now); err != nil || got.DayBytes != 120 {
		t.Fatalf("60 more with 40 left = %+v, %v; want it charged whole, since the team was under its cap", got, err)
	}
	_, err := charge(st, "team-a", 0, now)
	var capErr *store.DownloadCapError
	if !errors.As(err, &capErr) || capErr.UsedBytes != 120 || capErr.CapBytes != 100 || capErr.RetryAfter != 2*time.Hour {
		t.Fatalf("a download past the cap = %v, want a cap error retrying in 2h", err)
	}
	if _, err := st.ChargeDownload(context.Background(), store.DownloadCharge{
		Team: "team-a", Bytes: 500, Record: true, Now: now, FreeCapBytes: 100, FundedCapBytes: 1000,
	}); err != nil {
		t.Fatalf("recording a finished stream past the cap: %v", err)
	}
	if got, err := charge(st, "team-a", 100, now.Add(3*time.Hour)); err != nil || got.DayBytes != 100 {
		t.Fatalf("the next UTC day = %+v, %v; want a fresh cap", got, err)
	}
	if got, err := charge(st, "team-f", 900, now); err != nil || got.CapBytes != 1000 || got.Tier != store.TeamTierFunded {
		t.Fatalf("a funded team's 900 = %+v, %v; want the funded cap", got, err)
	}
	if got, err := charge(st, store.DefaultTeam, 1<<40, now); err != nil || got.CapBytes != 0 {
		t.Fatalf("the operator's team = %+v, %v; want no cap", got, err)
	}
}

func TestChargeDownloadRaceForTheLastBytesAdmitsExactlyOne(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	const readers, rounds = 5, 6
	for round := range rounds {
		team := store.Team(fmt.Sprintf("race-%d", round))
		freeTeam(t, st, team)
		if _, err := charge(st, team, 40, now); err != nil {
			t.Fatal(err)
		}
		warmPool(t, st, readers)
		won := race(t, readers, func() error {
			_, err := charge(st, team, 60, now)
			return err
		}, func(err error) bool { return errors.Is(err, store.ErrDownloadCap) })
		if won != 1 {
			t.Fatalf("round %d: %d of %d racing charges took the last 60 bytes, want exactly 1", round, won, readers)
		}
	}
}

// A writer settling one block while it takes the next pays one round trip:
// the commit and the next reservation share a transaction, and the commit
// lands even when the next block does not fit.
