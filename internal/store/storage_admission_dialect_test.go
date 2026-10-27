package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func bothDialects(t *testing.T, check func(*testing.T, *storetest.Target)) {
	t.Run("sqlite", func(t *testing.T) { check(t, storetest.NewSQLite(t)) })
	t.Run("postgres", func(t *testing.T) { check(t, storetest.NewPostgres(t)) })
}

// growthFixture is a started job of a signed-up team on Sparkwing Cloud defaults, with a handle on each of the three
// writes that grow its stored data.
type growthFixture struct {
	*storeRecoveryFixture
	attempt   runspec.AttemptIdentity
	retention string
	now       time.Time
}

func newGrowthFixture(t *testing.T, target *storetest.Target) *growthFixture {
	t.Helper()
	f := &growthFixture{storeRecoveryFixture: newStoreRecoveryFixtureAt(t, true, target)}
	f.s.EnableCloudStorage()
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	f.attempt = runspec.AttemptIdentity{Team: string(f.tenant.Team()), RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Attempt: int64(row.Attempt), Generation: int64(row.Generation)}
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, "SELECT log_retention_class FROM runs WHERE team=? AND id=?"), f.tenant.Team(), f.ref.RunID).Scan(&f.retention); err != nil {
		t.Fatal(err)
	}
	f.now = time.Now().UTC()
	f.s.SetTestClock(t, func() time.Time { return f.now })
	return f
}

// storeBytes seeds retained blob bytes the team already holds.
func (f *growthFixture) storeBytes(t *testing.T, digest string, size int64, created time.Time) {
	t.Helper()
	if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)"), f.tenant.Team(), strings.Repeat(digest, 64), size, created.UnixNano()); err != nil {
		t.Fatal(err)
	}
}

func (f *growthFixture) appendEvent(t *testing.T) error {
	t.Helper()
	return f.tenant.AppendEvent(t.Context(), string(f.ref.RunID), string(f.ref.NodeID), "note", []byte(`{"n":1}`))
}

func (f *growthFixture) reserveUpload(t *testing.T) error {
	t.Helper()
	authority := blob.Authority{CapabilityRef: f.holder.CapabilityPrefix, Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: f.attempt}}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: f.attempt.Team, RunID: f.attempt.RunID, Attempt: authority.Attempt, Principal: blob.Principal(f.holder.Holder), CapabilityRef: f.holder.CapabilityPrefix}
	_, err := f.tenant.ReserveBlobUpload(t.Context(), store.UploadReservationRequest{
		ID:        "growth_upload",
		Intent:    blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat("b", 64)), Size: 3},
		ExpiresAt: f.now.Add(time.Hour),
	})
	return err
}

func (f *growthFixture) recordSegment(t *testing.T) error {
	t.Helper()
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: f.attempt.Team, RunID: f.attempt.RunID, Attempt: &f.attempt}
	key, err := blob.LogObjectKey(subject, f.retention, 1)
	if err != nil {
		t.Fatal(err)
	}
	return f.tenant.RecordSegments(t.Context(), store.SegmentCommit{
		Subject: subject, Actor: f.holder, RetentionClass: f.retention,
		Segments: []runstate.Segment{{Index: 1, Offset: 0, Size: 3, StoredBytes: 3, Digest: strings.Repeat("c", 64), ObjectKey: key}},
	})
}

// growthRows counts what the three writes leave behind, so a refusal can be shown to record nothing.
func (f *growthFixture) growthRows(t *testing.T) [3]int {
	t.Helper()
	var out [3]int
	for i, table := range []string{"events", "uploads", "log_segments"} {
		if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, "SELECT COUNT(*) FROM "+table+" WHERE team=?"), f.tenant.Team()).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f *growthFixture) writes() []struct {
	name  string
	write func(*testing.T) error
} {
	return []struct {
		name  string
		write func(*testing.T) error
	}{{"event", f.appendEvent}, {"blob upload", f.reserveUpload}, {"log segment", f.recordSegment}}
}

func fundTeam(t *testing.T, tenant *store.Tenant, reference string) {
	t.Helper()
	if _, err := tenant.TestOnlySettlePaidCredit(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: reference, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
}

// On Sparkwing Cloud defaults a team that has never paid stores its free allowance and no more: run events, blob
// upload reservations and log segments past it are all refused and leave nothing behind, and the same writes are
// admitted once the team has credits.
func TestUnfundedCloudTeamCannotGrowStoragePastTheFreeAllowance(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		f.storeBytes(t, "a", 1<<30, f.now)
		for _, w := range f.writes() {
			before := f.growthRows(t)
			err := w.write(t)
			if !errors.Is(err, store.ErrInsufficientCredits) || !strings.Contains(err.Error(), "add credits") {
				t.Fatalf("unfunded %s past the free allowance = %v; want an insufficient-credits refusal naming the remedy", w.name, err)
			}
			if after := f.growthRows(t); after != before {
				t.Fatalf("refused %s recorded rows: before=%v after=%v", w.name, before, after)
			}
		}
		fundTeam(t, f.tenant, "pay-growth")
		for _, w := range f.writes() {
			if err := w.write(t); err != nil {
				t.Fatalf("funded %s: %v", w.name, err)
			}
		}
	})
}

// A held team grows no storage at all, even with credits and nothing stored: the hold refuses each of the three
// writes, and releasing it admits them.
func TestHeldTeamCannotGrowStorage(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-held")
		if _, err := f.tenant.HoldByOperator(t.Context(), "operator", "review"); err != nil {
			t.Fatal(err)
		}
		for _, w := range f.writes() {
			before := f.growthRows(t)
			err := w.write(t)
			var refusal *store.InsufficientCreditsError
			if !errors.As(err, &refusal) || refusal.Hold == "" {
				t.Fatalf("held %s = %v; want a hold refusal", w.name, err)
			}
			if after := f.growthRows(t); after != before {
				t.Fatalf("refused %s recorded rows: before=%v after=%v", w.name, before, after)
			}
		}
		if _, err := f.tenant.ReleaseOperatorHolds(t.Context(), "operator", "cleared"); err != nil {
			t.Fatal(err)
		}
		for _, w := range f.writes() {
			if err := w.write(t); err != nil {
				t.Fatalf("released %s: %v", w.name, err)
			}
		}
	})
}

// On Sparkwing Cloud defaults, with no rate or allowance ever written, a funded team pays the published rate for what
// it stores past the 1 GiB free allowance, and the console's standing reports the same allowance billing subtracts.
func TestCloudBillingChargesThePublishedRatePastTheFreeAllowance(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-billing")
		f.storeBytes(t, "a", 3*store.StorageBytesPerGB, f.now.Add(-48*time.Hour))
		settings, err := f.s.AsOperator().CreditSettings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		standing, err := f.tenant.StorageStanding(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if settings.StorageRateMicroPerGBDay != store.CloudStorageRateMicroPerGBDay || settings.StorageFreeAllowanceBytes != store.StorageBytesPerGB || standing.AllowanceBytes != settings.StorageFreeAllowanceBytes {
			t.Fatalf("settings=%+v standing allowance=%d; want the published rate and one 1 GiB allowance for both", settings, standing.AllowanceBytes)
		}
		if first, err := f.s.AsOperator().ChargeRetainedStorage(t.Context()); err != nil || len(first.Charges) != 0 {
			t.Fatalf("first pass = %+v, %v; want it to start the clock without billing history", first, err)
		}
		f.now = f.now.Add(24 * time.Hour)
		billed, err := f.s.AsOperator().ChargeRetainedStorage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(billed.Charges) != 1 || billed.ChargedMicro != 2*store.CloudStorageRateMicroPerGBDay || billed.Charges[0].StorageBytes != 2*store.StorageBytesPerGB {
			t.Fatalf("billing = %+v; want 2 GiB-days at the published rate", billed)
		}
	})
}

func (f *growthFixture) logSubject() blob.LogSubject {
	return blob.LogSubject{Kind: blob.LogJobAttempt, Team: f.attempt.Team, RunID: f.attempt.RunID, Attempt: &f.attempt}
}

func (f *growthFixture) retained(t *testing.T) int64 {
	t.Helper()
	n, err := f.tenant.StorageRetainedBytes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A signed log slot counts as retained from signing until its object is deleted, whether or not it is committed,
// so signing and uploading without ever committing cannot store past the free allowance; and the bytes keep
// counting after the URL expires.
func TestUncommittedLogSlotsStayCountedAndAdmitted(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		standing, err := f.tenant.StorageStanding(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		f.storeBytes(t, "a", standing.AllowanceBytes-standing.EventBytes-8, f.now)
		before := f.retained(t)
		if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 1, 8); err != nil {
			t.Fatalf("slot within the free allowance: %v", err)
		}
		if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 2, 8); !errors.Is(err, store.ErrInsufficientCredits) {
			t.Fatalf("second uncommitted slot past the allowance = %v; want a refusal", err)
		}
		f.now = f.now.Add(time.Hour)
		if grew := f.retained(t) - before; grew != 8 {
			t.Fatalf("an hour after signing, the uncommitted slot counts %d bytes; want 8", grew)
		}
	})
}

// A slot's size is fixed when it is first signed: signing it again for fewer bytes keeps counting the first size,
// signing it again for more never grows it, and committing a smaller object or sealing the stream never lowers it.
func TestLogSlotSizeIsFixedAtFirstSigning(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-slot")
		before := f.retained(t)
		for _, size := range []int64{limits.LogManifestBytes, 1, limits.LogManifestBytes + 1} {
			slot, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 0, size)
			if err != nil || slot.Size != limits.LogManifestBytes || slot.Committed {
				t.Fatalf("signing the manifest slot for %d bytes = %+v, %v; want its first size", size, slot, err)
			}
		}
		if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 1, 8); err != nil {
			t.Fatal(err)
		}
		if err := f.recordSegment(t); err != nil {
			t.Fatal(err)
		}
		if err := f.tenant.RecordSegments(t.Context(), store.SegmentCommit{Subject: f.logSubject(), Actor: f.holder, RetentionClass: f.retention, Final: true}); err != nil {
			t.Fatal(err)
		}
		if grew := f.retained(t) - before; grew != limits.LogManifestBytes+8 {
			t.Fatalf("committed slots count %d bytes; want their signed %d", grew, limits.LogManifestBytes+8)
		}
	})
}

// A segment larger than the slot it was signed for is refused at commit.
func TestLogSegmentLargerThanItsSlotIsRefused(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-larger")
		if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 1, 2); err != nil {
			t.Fatal(err)
		}
		if err := f.recordSegment(t); err == nil {
			t.Fatal("a 3-byte segment was committed into a 2-byte slot")
		}
	})
}

// A segment whose commit landed but whose acknowledgement was lost is reported committed when its slot is signed
// again, so the uploader moves on instead of failing.
func TestCommittedLogSlotReportsCommittedOnResign(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-ack")
		if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 1, 3); err != nil {
			t.Fatal(err)
		}
		if err := f.recordSegment(t); err != nil {
			t.Fatal(err)
		}
		if slot, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, 1, 3); err != nil || !slot.Committed {
			t.Fatalf("re-signing a committed slot = %+v, %v; want it reported committed", slot, err)
		}
	})
}

type failingLogDeleter struct{}

func (failingLogDeleter) DeleteLogObject(context.Context, string, string, string) error {
	return errors.New("object store unavailable")
}

// Sparkwing deletes a log object once its slot is older than the retention class, keyed on the slot's own signing
// time, and only then stops counting it. A slot signed later in the same stream stays, and a failed delete keeps
// counting its bytes for the next pass.
func TestExpiredLogObjectsStopCounting(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		f := newGrowthFixture(t, target)
		fundTeam(t, f.tenant, "pay-expiry")
		dir := t.TempDir()
		objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "http://objects", SigningKey: []byte(strings.Repeat("k", 32))})
		if err != nil {
			t.Fatal(err)
		}
		days, ok := blob.LogClassDays(f.retention)
		if !ok {
			t.Fatalf("invalid retention class %q", f.retention)
		}
		before := f.retained(t)
		put := func(index, size int64) string {
			t.Helper()
			if _, err := f.tenant.OpenLogSlot(t.Context(), f.logSubject(), f.holder, index, size); err != nil {
				t.Fatal(err)
			}
			key, err := blob.LogObjectKey(f.logSubject(), f.retention, index)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
				t.Fatal(err)
			}
			return key
		}
		old, recent := put(1, 8), put(2, 5)
		// Age the slots rather than the clock, which would end the job's authority: the first was signed just past
		// its class, the second just inside it.
		for index, age := range map[int64]time.Duration{1: time.Duration(days)*24*time.Hour + time.Hour, 2: time.Duration(days)*24*time.Hour - time.Hour} {
			if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, "UPDATE log_slots SET created_at=? WHERE team=? AND n=?"), f.now.Add(-age).UnixNano(), f.tenant.Team(), index); err != nil {
				t.Fatal(err)
			}
		}
		operator := f.s.AsOperator()
		if _, err := operator.DeleteExpiredLogObjects(t.Context(), 100, failingLogDeleter{}); err == nil {
			t.Fatal("a failed object delete was reported as success")
		}
		if grew := f.retained(t) - before; grew != 13 {
			t.Fatalf("after a failed delete the slots count %d bytes; want 13", grew)
		}
		if n, err := operator.DeleteExpiredLogObjects(t.Context(), 100, objects); err != nil || n != 1 {
			t.Fatalf("expiry deleted %d objects, %v; want the one past its class", n, err)
		}
		exists := func(key string) bool {
			_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key)))
			return err == nil
		}
		if exists(old) || !exists(recent) {
			t.Fatal("expiry deleted the wrong log object")
		}
		if grew := f.retained(t) - before; grew != 5 {
			t.Fatalf("after expiry the slots count %d bytes; want the recent 5", grew)
		}
	})
}
