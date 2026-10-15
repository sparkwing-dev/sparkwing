package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSourceBundleBindsOneRun(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	tn := freeTeam(t, st, "team-source")
	now := time.Now()
	newSource := func(nonce string) store.Upload {
		t.Helper()
		digest := strings.Repeat("a", 64)
		u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-source", Kind: store.StorageCache, Key: "sources/" + digest + "/" + nonce,
			Size: 10, SHA256: digest, Principal: "owner", ClaimPrefix: "swu_owner", Provenance: "local", Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CommitUpload(t.Context(), u.Team, u.ID, u.Principal, now); err != nil {
			t.Fatal(err)
		}
		return u
	}
	bound := newSource(strings.Repeat("2", 32))
	if err := tn.CreateSourceTriggerWithRun(t.Context(),
		store.Trigger{ID: "run-source", Pipeline: "build", TriggerSource: "pipeline-working-tree@test", CreatedAt: now},
		store.Run{ID: "run-source", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now},
		bound.Key, "owner", "swu_owner"); err != nil {
		t.Fatal(err)
	}
	if err := tn.CreateSourceTriggerWithRun(t.Context(),
		store.Trigger{ID: "run-reuse", Pipeline: "build", CreatedAt: now},
		store.Run{ID: "run-reuse", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now},
		bound.Key, "owner", "swu_owner"); !errors.Is(err, store.ErrSourceAlreadyBound) {
		t.Fatalf("second source bind = %v", err)
	}
}

func TestSourceReserveAcquiresOnlyOneFreeTeamSlotBeforeAnyRun(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	if err := st.SetFreeTeamSlots(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	teamHandle(t, st, "source-a")
	teamHandle(t, st, "source-b")
	request := store.UploadRequest{
		Team: "source-a", Kind: store.StorageCache,
		Key: "sources/" + strings.Repeat("a", 64) + "/" + strings.Repeat("1", 32), Size: 10,
		SHA256: strings.Repeat("a", 64), Principal: "owner", ClaimPrefix: "swu_owner", Provenance: "local",
	}
	first, err := st.ReserveUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if taken, _, err := st.FreeSlots(t.Context()); err != nil || taken != 0 {
		t.Fatalf("slots after a reservation = %d, %v; want none until a byte is committed", taken, err)
	}
	if err := st.CommitUpload(t.Context(), "source-a", first.ID, first.Principal, time.Now()); err != nil {
		t.Fatal(err)
	}
	request.Key = "sources/" + strings.Repeat("a", 64) + "/" + strings.Repeat("2", 32)
	if _, err := st.ReserveUpload(t.Context(), request); err != nil {
		t.Fatalf("same team took a second slot: %v", err)
	}
	if taken, _, err := st.FreeSlots(t.Context()); err != nil || taken != 1 {
		t.Fatalf("slots = %d, %v", taken, err)
	}
	request.Team = "source-b"
	if _, err := st.ReserveUpload(t.Context(), request); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("new team pretrigger upload with no slot = %v", err)
	}
	var runs int
	if err := st.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team = 'source-a'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("pretrigger source charged %d runs, %v", runs, err)
	}
}

func TestSourceCanBindForTwentyFourHoursAfterCommit(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	tn := freeTeam(t, st, "team-source")
	reservedAt := time.Now().Add(-25 * time.Hour)
	committedAt := reservedAt.Add(23 * time.Hour)
	digest := strings.Repeat("a", 64)
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-source", Kind: store.StorageCache,
		Key: "sources/" + digest + "/" + strings.Repeat("1", 32), Size: 10,
		SHA256: digest, Principal: "owner", ClaimPrefix: "swu_owner", Provenance: "local", Now: reservedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitUpload(t.Context(), u.Team, u.ID, u.Principal, committedAt); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err = tn.CreateSourceTriggerWithRun(t.Context(),
		store.Trigger{ID: "run-late", Pipeline: "build", CreatedAt: now},
		store.Run{ID: "run-late", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now},
		u.Key, "owner", "swu_owner")
	if err != nil {
		t.Fatalf("source still within 24 hours after commit could not bind: %v", err)
	}
}

func TestExpiredSourceCannotBindWithEarlierTriggerTimestamp(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	tn := freeTeam(t, st, "team-source")
	committedAt := time.Now().Add(-25 * time.Hour)
	reservedAt := committedAt.Add(-time.Hour)
	digest := strings.Repeat("a", 64)
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-source", Kind: store.StorageCache,
		Key: "sources/" + digest + "/" + strings.Repeat("1", 32), Size: 10,
		SHA256: digest, Principal: "owner", ClaimPrefix: "swu_owner", Provenance: "local", Now: reservedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitUpload(t.Context(), u.Team, u.ID, u.Principal, committedAt); err != nil {
		t.Fatal(err)
	}
	stampedBeforeExpiry := committedAt.Add(23 * time.Hour)
	err = tn.CreateSourceTriggerWithRun(t.Context(),
		store.Trigger{ID: "run-stale", Pipeline: "build", CreatedAt: stampedBeforeExpiry},
		store.Run{ID: "run-stale", Pipeline: "build", Status: "pending", CreatedAt: stampedBeforeExpiry, StartedAt: stampedBeforeExpiry},
		u.Key, "owner", "swu_owner")
	if !errors.Is(err, store.ErrSourceAlreadyBound) {
		t.Fatalf("expired source bound with stale trigger timestamp: %v", err)
	}
}

func TestDirectUploadRefusesEmptyAndCapsPendingPerTeam(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	freeTeam(t, st, "team-a")
	freeTeam(t, st, "team-b")
	now := time.Now()
	reserve := func(team store.Team, i int, size int64, at time.Time) (store.Upload, error) {
		digest := fmt.Sprintf("%064x", i)
		return st.ReserveUpload(t.Context(), store.UploadRequest{
			Team: team, RunID: "run-" + string(team), Kind: store.StorageCache, Key: "artifacts/blobs/" + digest,
			Size: size, SHA256: digest, Principal: "runner", Provenance: "cloud", Now: at,
		})
	}
	if _, err := reserve("team-a", 0, 0, now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("zero-byte reserve: %v", err)
	}
	var first store.Upload
	for i := range store.DirectUploadMaxPending {
		u, err := reserve("team-a", i+1, 1, now)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if i == 0 {
			first = u
		}
	}
	if _, err := reserve("team-a", 1000, 1, now); !errors.Is(err, store.ErrTooManyPendingUploads) {
		t.Fatalf("reserve past the pending cap: %v", err)
	}
	if _, err := reserve("team-b", 1000, 1, now); err != nil {
		t.Fatalf("another team's reserve: %v", err)
	}
	if err := st.CommitUpload(t.Context(), "team-a", first.ID, first.Principal, now); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve("team-a", 1001, 1, now); err != nil {
		t.Fatalf("reserve after a commit freed a slot: %v", err)
	}
	if _, err := reserve("team-a", 1002, 1, now); !errors.Is(err, store.ErrTooManyPendingUploads) {
		t.Fatalf("reserve past the refilled cap: %v", err)
	}
	if _, err := reserve("team-a", 1003, 1, now.Add(store.DirectUploadTTL+time.Second)); err != nil {
		t.Fatalf("reserve after the pending uploads expired: %v", err)
	}
}

func TestReleasedDirectUploadFreesItsPendingSlot(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1<<30)
	freeTeam(t, st, "team-a")
	now := time.Now()
	reserve := func(i int) (store.Upload, error) {
		digest := fmt.Sprintf("%064x", i)
		return st.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "artifacts/blobs/" + digest,
			Size: 1, SHA256: digest, Principal: "runner", Provenance: "cloud", Now: now,
		})
	}
	for i := range store.DirectUploadMaxPending {
		u, err := reserve(i + 1)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if err := st.ReleaseStorage(t.Context(), u.Team, u.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reserve(1000); err != nil {
		t.Fatalf("reserve after every earlier reservation was released: %v", err)
	}
	if _, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageLogs, Key: "artifacts/blobs/" + fmt.Sprintf("%064x", 2000),
		Size: 1, SHA256: fmt.Sprintf("%064x", 2000), Principal: "runner", Provenance: "cloud", Now: now,
	}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a logs-kind upload = %v, want ErrInvalidInput", err)
	}
}
