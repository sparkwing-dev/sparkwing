package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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
