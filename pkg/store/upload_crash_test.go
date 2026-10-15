package store

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

const uploadCrashExit = 86

func TestBlobUploadCrashReplay(t *testing.T) {
	if len(os.Args) >= 5 && os.Args[len(os.Args)-4] == "--" && os.Args[len(os.Args)-3] == "store-upload-crash" {
		uploadCrashCheckpoint(t, os.Args[len(os.Args)-2], os.Args[len(os.Args)-1])
		return
	}
	s, tenant, request := uploadFixture(t)
	path := blobAccountingDiskStore(t, s)
	request.ID = "committed"
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "request.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tenant.SetStorageQuota(t.Context(), StorageQuota{MaxBytesPerRun: 4, MaxObjectsPerRun: 2}); err != nil {
		t.Fatal(err)
	}
	committed, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	standing, err := tenant.StorageStanding(t.Context())
	if err != nil || standing.BlobBytes != 0 || standing.ReservedBlobBytes != 3 {
		t.Fatalf("upload hold missing from standing: %+v: %v", standing, err)
	}
	request.ID, request.Intent.Digest = "aborted", blob.Digest(strings.Repeat("b", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("pending upload bypassed per-run capacity: %v", err)
	}
	request.Intent.Size = 1
	aborted, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	restart := func(point string) {
		t.Helper()
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBlobUploadCrashReplay$", "--", "store-upload-crash", path, point)
		output, err := cmd.CombinedOutput()
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != uploadCrashExit {
			t.Fatalf("upload checkpoint %s failed: %v\n%s", point, err, output)
		}
		s.db = blobAccountingDB(t, path)
	}
	restart("before_commit")
	pending, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, committed.ID)
	if err != nil || pending.CommittedAt != nil {
		t.Fatalf("before-commit crash changed ticket: %+v: %v", pending, err)
	}
	restart("after_commit")
	paid, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, committed.ID)
	if err != nil || paid.CommittedAt == nil {
		t.Fatalf("after-commit crash lost ticket: %+v: %v", paid, err)
	}
	restart("before_abort")
	if _, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, aborted.ID); err != nil {
		t.Fatalf("before-abort crash released ticket: %v", err)
	}
	restart("after_abort")
	if _, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, aborted.ID); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("after-abort crash left ticket usable: %v", err)
	}
	commit := verifiedUpload(committed, request.Intent.Authority)
	for range 2 {
		if _, err := tenant.CommitBlobUpload(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
		if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, aborted.ID); err != nil {
			t.Fatal(err)
		}
	}
	request.ID, request.Intent.Digest, request.Intent.Size = "duplicate", committed.Blob.Digest, committed.Blob.Size
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	commit.TicketID, commit.Verified, commit.ReplicaID = duplicate.ID, duplicate.Blob, "replica-b"
	if _, err := tenant.CommitBlobUpload(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	request.ID, request.Intent.Digest, request.Intent.Size = "remaining", blob.Digest(strings.Repeat("c", 64)), 1
	remaining, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatalf("abort/replay lost remaining capacity: %v", err)
	}
	request.ID, request.Intent.Digest = "excess", blob.Digest(strings.Repeat("d", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("replay manufactured capacity: %v", err)
	}
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, remaining.ID); err != nil {
		t.Fatal(err)
	}
	usage, err := tenant.StorageUsageFor(t.Context(), "run", StorageMonth(s.now()))
	if err != nil || usage.RunBytes != 3 || usage.RunObjects != 1 || usage.MonthBytes != 3 || usage.MonthObjects != 1 {
		t.Fatalf("upload replay/dedup usage=%+v: %v", usage, err)
	}
	for quota, want := range map[string]int64{"storage": 3, "storage.run.bytes:run": 3, "storage.run.objects:run": 1} {
		var used, reserved int64
		if err := s.db.QueryRow("SELECT used,reserved FROM quota_admission WHERE team='a' AND quota=?", quota).Scan(&used, &reserved); err != nil {
			t.Fatal(err)
		}
		if used != want || reserved != 0 {
			t.Fatalf("settled %s capacity=(%d,%d), want (%d,0)", quota, used, reserved, want)
		}
	}
	var held int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM quota_reservations WHERE team='a' AND state='held'").Scan(&held); err != nil || held != 0 {
		t.Fatalf("upload settlement leaked %d holds: %v", held, err)
	}
	standing, err = tenant.StorageStanding(t.Context())
	if err != nil || standing.BlobBytes != 3 || standing.ReservedBlobBytes != 0 || standing.LogBytes != 0 {
		t.Fatalf("reopened standing double-counted upload settlement: %+v: %v", standing, err)
	}
}

func uploadCrashCheckpoint(t *testing.T, path, point string) {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(path), "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request UploadReservationRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: func() time.Time { return request.ExpiresAt.Add(-time.Hour) }}
	team, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	switch point {
	case "before_commit", "before_abort":
	case "after_commit":
		ticket, err := team.LookupBlobUpload(t.Context(), request.Intent.Authority, request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := team.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
			t.Fatal(err)
		}
	case "after_abort":
		if err := team.AbortBlobUpload(t.Context(), request.Intent.Authority, "aborted"); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown upload crash checkpoint %q", point)
	}
	runtime.KeepAlive(s)
	os.Exit(uploadCrashExit)
}
