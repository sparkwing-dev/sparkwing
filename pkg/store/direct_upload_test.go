package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestDirectUploadReservesAndPublishesOnlyAfterCommit(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	now := time.Now()
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "artifacts/blobs/" + strings.Repeat("a", 64),
		Size: 12 << 10, SHA256: strings.Repeat("a", 64), Principal: "runner-a", Provenance: "cloud", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommittedObject(t.Context(), "team-a", u.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("uncommitted object visible: %v", err)
	}
	if _, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "artifacts/blobs/" + strings.Repeat("b", 64),
		Size: 1, SHA256: strings.Repeat("b", 64), Principal: "runner-a", Provenance: "cloud", Now: now,
	}); !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("oversize reserve: %v", err)
	}
	if err := st.CommitUpload(t.Context(), "team-b", u.ID, u.Principal, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-team commit: %v", err)
	}
	if err := st.CommitUpload(t.Context(), "team-a", u.ID, u.Principal, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.CommittedObject(t.Context(), "team-a", u.Key)
	if err != nil || got.Principal != "runner-a" || got.Provenance != "cloud" || got.SHA256 != u.SHA256 {
		t.Fatalf("published object: %+v, %v", got, err)
	}
	if v := usageOf(t, st, "team-a", store.StorageCache); v.UsedBytes != u.Size || v.ReservedBytes != 0 {
		t.Fatalf("storage after commit: %+v", v)
	}
	if err := st.CommitUpload(context.Background(), "team-a", u.ID, u.Principal, now); err != nil {
		t.Fatalf("retry commit: %v", err)
	}
}

func TestSourceBundleOneRunAndRetention(t *testing.T) {
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
	orphan := newSource(strings.Repeat("1", 32))
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
	if rows, err := st.ClaimExpiredSourceBundles(t.Context(), now.Add(23*time.Hour)); err != nil || len(rows) != 0 {
		t.Fatalf("premature source expiry = %v, %v", rows, err)
	}
	if rows, err := st.ClaimExpiredSourceBundles(t.Context(), now.Add(25*time.Hour)); err != nil || len(rows) != 1 || rows[0].ID != orphan.ID {
		t.Fatalf("orphan expiry with pending run = %v, %v", rows, err)
	}
	late := now.Add(25 * time.Hour)
	if err := tn.CreateSourceTriggerWithRun(t.Context(),
		store.Trigger{ID: "run-too-late", Pipeline: "build", CreatedAt: late},
		store.Run{ID: "run-too-late", Pipeline: "build", Status: "pending", CreatedAt: late, StartedAt: late},
		orphan.Key, "owner", "swu_owner"); !errors.Is(err, store.ErrSourceAlreadyBound) {
		t.Fatalf("expired orphan source bound to a run: %v", err)
	}
	// A row written before terminal-status validation can carry this invalid pair.
	if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st,
		`UPDATE runs SET finished_at = ? WHERE id = ? AND team = ?`),
		time.Now().UnixNano(), "run-source", "team-source"); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.ClaimExpiredSourceBundles(t.Context(), time.Now().Add(25*time.Hour)); err != nil || len(rows) != 1 {
		t.Fatalf("nonterminal source with a finish timestamp was claimed: %v, %v", rows, err)
	}
	if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st,
		`UPDATE runs SET finished_at = NULL WHERE id = ? AND team = ?`), "run-source", "team-source"); err != nil {
		t.Fatal(err)
	}
	if err := tn.FinishRun(t.Context(), "run-source", "success", ""); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.ClaimExpiredSourceBundles(t.Context(), time.Now().Add(25*time.Hour)); err != nil || len(rows) != 2 {
		t.Fatalf("terminal source expiry = %v, %v", rows, err)
	}
	if err := st.DeleteSourceBundleRows(t.Context(), bound); err != nil {
		t.Fatal(err)
	}
	if exists, err := st.SourceBoundToRun(t.Context(), "team-source", bound.Key, "run-source"); err != nil || exists {
		t.Fatalf("deleted source bind = %t, %v", exists, err)
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
	if _, err := st.ReserveUpload(t.Context(), request); err != nil {
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
	if rows, err := st.ClaimExpiredSourceBundles(t.Context(), now); err != nil || len(rows) != 0 {
		t.Fatalf("two-hour-old committed source expired: %v, %v", rows, err)
	}
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

func TestSourcePruneClaimAndBinderChooseOneOwner(t *testing.T) {
	for _, pruneFirst := range []bool{false, true} {
		name := "binder-first"
		if pruneFirst {
			name = "prune-first"
		}
		t.Run(name, func(t *testing.T) {
			st := storetest.Open(t)
			setFreeAllowance(t, st, 1<<30)
			tn := freeTeam(t, st, "team-source")
			reservedAt := time.Now().Add(-time.Hour)
			committedAt := reservedAt.Add(time.Minute)
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
			bind := func() error {
				at := time.Now()
				return tn.CreateSourceTriggerWithRun(t.Context(),
					store.Trigger{ID: "run-bound", Pipeline: "build", CreatedAt: at},
					store.Run{ID: "run-bound", Pipeline: "build", Status: "pending", CreatedAt: at, StartedAt: at},
					u.Key, "owner", "swu_owner")
			}
			claim := func() []store.Upload {
				t.Helper()
				rows, err := st.ClaimExpiredSourceBundles(t.Context(), committedAt.Add(25*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				return rows
			}
			if pruneFirst {
				if rows := claim(); len(rows) != 1 || rows[0].ID != u.ID {
					t.Fatalf("first prune claim = %+v", rows)
				}
				if err := bind(); !errors.Is(err, store.ErrSourceAlreadyBound) {
					t.Fatalf("source bound after prune claim: %v", err)
				}
				if rows := claim(); len(rows) != 1 || rows[0].ID != u.ID {
					t.Fatalf("failed S3 delete did not leave a retry candidate: %+v", rows)
				}
				if _, err := st.CommittedObject(t.Context(), u.Team, u.Key); err != nil {
					t.Fatalf("marked source lost object row before S3 delete: %v", err)
				}
				if got := usageOf(t, st, u.Team, store.StorageCache); got.UsedBytes != u.Size {
					t.Fatalf("marked source released quota before S3 delete: %+v", got)
				}
			} else {
				if err := bind(); err != nil {
					t.Fatal(err)
				}
				if rows := claim(); len(rows) != 0 {
					t.Fatalf("prune claimed pending run's source: %+v", rows)
				}
			}
		})
	}
}

func TestPruneExpiredCacheObjectsIncludesDefaultTeam(t *testing.T) {
	st := storetest.Open(t)
	old := time.Now().Add(-31 * 24 * time.Hour)
	key := "artifacts/blobs/" + strings.Repeat("a", 64)
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: store.DefaultTeam, RunID: "run-operator", Kind: store.StorageCache,
		Key: key, Size: 1, SHA256: strings.Repeat("a", 64), Principal: "operator", Provenance: "local", Now: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitUpload(t.Context(), store.DefaultTeam, u.ID, u.Principal, old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneExpiredCacheObjects(t.Context(), time.Now()); err != nil || n != 1 {
		t.Fatalf("expired default cache rows = %d, %v, want one", n, err)
	}
	if _, err := st.CommittedObject(t.Context(), store.DefaultTeam, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired default cache object is still committed: %v", err)
	}
}

func TestCloudBinaryLookupRefusesLocalProvenanceUntilTeamTrustsIt(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	input := "01234567-89abcdef"
	digest := strings.Repeat("a", 64)
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "bin/" + input + "/" + digest,
		Size: 10, SHA256: digest, Principal: "laptop", Provenance: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitUpload(t.Context(), "team-a", u.ID, u.Principal, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BinaryObject(t.Context(), "team-a", input, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cloud lookup of local binary = %v", err)
	}
	if got, err := st.BinaryObject(t.Context(), "team-a", input, false); err != nil || got.Provenance != "local" {
		t.Fatalf("local lookup = %+v, %v", got, err)
	}
	if err := st.SetTrustLocalBuilds(t.Context(), "team-a", true); err != nil {
		t.Fatal(err)
	}
	if got, err := st.BinaryObject(t.Context(), "team-a", input, true); err != nil || got.Key != u.Key {
		t.Fatalf("trusted cloud lookup = %+v, %v", got, err)
	}
}

func TestSameBinaryCanBeCommittedByLocalAndCloudRunners(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	input := "01234567-89abcdef"
	digest := strings.Repeat("a", 64)
	key := "bin/" + input + "/" + digest
	for _, provenance := range []string{"local", "cloud"} {
		u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
			Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: key,
			Size: 10, SHA256: digest, Principal: provenance, Provenance: provenance,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CommitUpload(t.Context(), "team-a", u.ID, u.Principal, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	cloud, err := st.BinaryObject(t.Context(), "team-a", input, true)
	if err != nil || cloud.Provenance != "cloud" {
		t.Fatalf("cloud lookup = %+v, %v", cloud, err)
	}
	if got, err := st.CommittedObjectFor(t.Context(), "team-a", key, true); err != nil || got.Provenance != "cloud" {
		t.Fatalf("cloud object = %+v, %v", got, err)
	}
}

func TestCloudBinaryLookupNeverReturnsLocalDuringCloudExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: concurrent binary provenance expiry")
	}
	st := storetest.Open(t)
	key := "bin/01234567-89abcdef/" + strings.Repeat("a", 64)
	digest := strings.Repeat("a", 64)
	insert := `INSERT INTO data_objects (team, key, store, size, sha256, principal, provenance, committed_at)
		VALUES ('team-a', ?, 'cache', 1, ?, 'runner', ?, ?)
		ON CONFLICT DO NOTHING`
	if _, err := st.DB().Exec(insert, key, digest, "local", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(insert, key, digest, "cloud", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if got, err := st.BinaryObject(t.Context(), "team-a", "01234567-89abcdef", true); err != nil || got.Provenance != "cloud" {
		t.Fatalf("initial cloud lookup = %+v, %v", got, err)
	}
	done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 2000 {
			if _, err := st.DB().ExecContext(t.Context(),
				`DELETE FROM data_objects WHERE team = 'team-a' AND key = ? AND provenance = 'cloud'`, key); err != nil {
				done <- err
				return
			}
			if _, err := st.DB().ExecContext(t.Context(), insert, key, digest, "cloud", time.Now().UnixNano()); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer wg.Wait()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
		got, err := st.BinaryObject(t.Context(), "team-a", "01234567-89abcdef", true)
		switch {
		case err == nil && got.Provenance == "cloud":
		case err == nil:
			t.Fatalf("untrusted cloud reader received %q binary", got.Provenance)
		case errors.Is(err, store.ErrNotFound):
		default:
			t.Fatal(err)
		}
	}
}

func TestExpiredDirectUploadReleasesItsReservationWithoutPublishing(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 16<<10)
	freeTeam(t, st, "team-a")
	now := time.Now()
	u, err := st.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "artifacts/blobs/" + strings.Repeat("c", 64),
		Size: 100, SHA256: strings.Repeat("c", 64), Principal: "runner", Provenance: "cloud", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.ReservedBytes != 100 {
		t.Fatalf("reserved = %+v", got)
	}
	expired := now.Add(store.DirectUploadTTL)
	if _, err := st.ReleaseExpiredStorage(t.Context(), expired); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneExpiredUploads(t.Context(), expired); err != nil || n != 1 {
		t.Fatalf("pruned = %d, %v", n, err)
	}
	if got := usageOf(t, st, "team-a", store.StorageCache); got.ReservedBytes != 0 || got.UsedBytes != 0 {
		t.Fatalf("expired usage = %+v", got)
	}
	if _, err := st.UploadForTeam(t.Context(), "team-a", u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired upload = %v", err)
	}
	if _, err := st.CommittedObject(t.Context(), "team-a", u.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired object = %v", err)
	}
}
