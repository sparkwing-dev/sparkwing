package store_test

import (
	"context"
	"errors"
	"strings"
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
	if err := st.CommitUpload(t.Context(), "team-b", u.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-team commit: %v", err)
	}
	if err := st.CommitUpload(t.Context(), "team-a", u.ID, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.CommittedObject(t.Context(), "team-a", u.Key)
	if err != nil || got.Principal != "runner-a" || got.Provenance != "cloud" || got.SHA256 != u.SHA256 {
		t.Fatalf("published object: %+v, %v", got, err)
	}
	if v := usageOf(t, st, "team-a", store.StorageCache); v.UsedBytes != u.Size || v.ReservedBytes != 0 {
		t.Fatalf("storage after commit: %+v", v)
	}
	if err := st.CommitUpload(context.Background(), "team-a", u.ID, now); err != nil {
		t.Fatalf("retry commit: %v", err)
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
	if err := st.CommitUpload(t.Context(), "team-a", u.ID, time.Now()); err != nil {
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
		if err := st.CommitUpload(t.Context(), "team-a", u.ID, time.Now()); err != nil {
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
