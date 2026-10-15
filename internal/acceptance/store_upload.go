package acceptance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreUploadCrashExit identifies an abrupt fixture exit after its selected checkpoint.
const StoreUploadCrashExit = 86

// StoreUploadCrashDriver runs the fixture in a child process and waits for its abrupt exit.
type StoreUploadCrashDriver func(context.Context, string, string) error

func checkStoreUploadBoundary(ctx context.Context, root string, crash StoreUploadCrashDriver) (err error) {
	if crash == nil {
		return errors.New("upload settlement proof requires a process crash driver")
	}
	path := filepath.Join(root, "store-upload.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := seedStoreUploadBoundary(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if err := tenant.SetStorageQuota(ctx, store.StorageQuota{MaxBytesPerRun: 4, MaxObjectsPerRun: 2}); err != nil {
		return err
	}
	committed, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	request.ID = "aborted"
	request.Intent.Digest = blob.Digest(strings.Repeat("b", 64))
	if _, err := tenant.ReserveBlobUpload(ctx, request); !errors.Is(err, store.ErrStorageQuota) {
		return fmt.Errorf("pending upload bypassed per-run capacity: %v", err)
	}
	request.Intent.Size = 1
	aborted, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	commit := store.VerifiedUploadCommit{TicketID: committed.ID, Authority: request.Intent.Authority, Verified: committed.Blob, ReplicaID: "replica-a"}
	restart := func(point string) error {
		if err := s.Close(); err != nil {
			return err
		}
		if err := crash(ctx, root, point); err != nil {
			return err
		}
		reopened, err := store.Open(path)
		if err != nil {
			return err
		}
		s = reopened
		tenant, err = s.ForTeam(ctx, store.DefaultTeam)
		return err
	}
	if err := restart("before_commit"); err != nil {
		return err
	}
	pending, err := tenant.LookupBlobUpload(ctx, request.Intent.Authority, committed.ID)
	if err != nil || pending.CommittedAt != nil {
		return fmt.Errorf("before-commit crash changed ticket: %+v: %v", pending, err)
	}
	if err := restart("after_commit"); err != nil {
		return err
	}
	paid, err := tenant.LookupBlobUpload(ctx, request.Intent.Authority, committed.ID)
	if err != nil || paid.CommittedAt == nil {
		return fmt.Errorf("after-commit crash lost ticket: %+v: %v", paid, err)
	}
	if err := restart("before_abort"); err != nil {
		return err
	}
	if _, err := tenant.LookupBlobUpload(ctx, request.Intent.Authority, aborted.ID); err != nil {
		return fmt.Errorf("before-abort crash released ticket: %w", err)
	}
	if err := restart("after_abort"); err != nil {
		return err
	}
	if _, err := tenant.LookupBlobUpload(ctx, request.Intent.Authority, aborted.ID); !errors.Is(err, store.ErrLockHeld) {
		return fmt.Errorf("after-abort crash left ticket usable: %v", err)
	}
	for range 2 {
		if _, err := tenant.CommitBlobUpload(ctx, commit); err != nil {
			return err
		}
		if err := tenant.AbortBlobUpload(ctx, request.Intent.Authority, aborted.ID); err != nil {
			return err
		}
	}
	request.ID, request.Intent.Digest, request.Intent.Size = "duplicate", committed.Blob.Digest, committed.Blob.Size
	duplicate, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	commit.TicketID, commit.Verified, commit.ReplicaID = duplicate.ID, duplicate.Blob, "replica-b"
	if _, err := tenant.CommitBlobUpload(ctx, commit); err != nil {
		return err
	}
	request.ID, request.Intent.Digest, request.Intent.Size = "remaining", blob.Digest(strings.Repeat("c", 64)), 1
	remaining, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return fmt.Errorf("abort/replay lost remaining capacity: %w", err)
	}
	request.ID, request.Intent.Digest = "excess", blob.Digest(strings.Repeat("d", 64))
	if _, err := tenant.ReserveBlobUpload(ctx, request); !errors.Is(err, store.ErrStorageQuota) {
		return fmt.Errorf("replay manufactured capacity: %v", err)
	}
	if err := tenant.AbortBlobUpload(ctx, request.Intent.Authority, remaining.ID); err != nil {
		return err
	}
	usage, err := tenant.StorageUsageFor(ctx, "upload", store.StorageMonth(time.Now()))
	if err != nil {
		return err
	}
	if usage.RunBytes != 3 || usage.RunObjects != 1 || usage.MonthBytes != 3 || usage.MonthObjects != 1 {
		return fmt.Errorf("upload replay/dedup usage=%+v, want three bytes and one object", usage)
	}
	for quota, want := range map[string]int64{"storage": 3, "storage.run.bytes:upload": 3, "storage.run.objects:upload": 1} {
		var used, reserved int64
		if err := s.DB().QueryRowContext(ctx, "SELECT used,reserved FROM quota_admission WHERE team=? AND quota=?", store.DefaultTeam, quota).Scan(&used, &reserved); err != nil {
			return err
		}
		if used != want || reserved != 0 {
			return fmt.Errorf("settled %s capacity=(%d,%d), want (%d,0)", quota, used, reserved, want)
		}
	}
	var held int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_reservations WHERE team=? AND state='held'", store.DefaultTeam).Scan(&held); err != nil {
		return err
	}
	if held != 0 {
		return fmt.Errorf("upload settlement leaked %d holds", held)
	}
	return nil
}

// DriveStoreUploadCrash leaves its store open so the caller can exit abruptly.
// The fixture process must exit with StoreUploadCrashExit before closing the handle.
func DriveStoreUploadCrash(ctx context.Context, root, point string) (*store.Store, error) {
	s, err := store.Open(filepath.Join(root, "store-upload.db"))
	if err != nil {
		return nil, err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err == nil {
		authority := blob.Authority{CapabilityRef: "upload-boundary"}
		switch point {
		case "before_commit", "before_abort":
		case "after_commit":
			var ticket store.UploadReservation
			ticket, err = tenant.LookupBlobUpload(ctx, authority, "committed")
			if err == nil {
				_, err = tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{TicketID: ticket.ID, Authority: authority, Verified: ticket.Blob, ReplicaID: "replica-a"})
			}
		case "after_abort":
			err = tenant.AbortBlobUpload(ctx, authority, "aborted")
		default:
			err = fmt.Errorf("unknown upload crash checkpoint %q", point)
		}
	}
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

// safety: preseeded authority isolates persistence from root provisioning and physical verification.
func seedStoreUploadBoundary(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	now := time.Now().UTC()
	c := capability.Capability{
		Digest: sha256.Sum256([]byte("upload-boundary")), Prefix: "upload-boundary", Team: string(store.DefaultTeam),
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "publisher"}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.DataRead, capability.DataWrite}, Issuance: capability.Delegate,
		Binding: capability.Binding{Kind: capability.BindingRun, RunID: "upload"}, IssuedAt: now,
		ReservedPools: []string{}, Repositories: []string{},
		Data: &capability.DataScope{Use: capability.DataPublish, RepositoryKey: "gh:1", RunID: "upload"},
	}
	record := store.CapabilityRecord{Version: 1, Value: c}
	if err := record.Validate(); err != nil {
		return store.UploadReservationRequest{}, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,'upload',1,'fixture','running',?,'fixture',?,'gh:1','host','publish',?,'publisher',?,'r30',30)`, store.DefaultTeam, []byte(`{"version":1}`), now.UnixNano(), []byte(`[]`), now.UnixNano()); err != nil {
		return store.UploadReservationRequest{}, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,reserved_pools,created_at,created_by,repo_key,repo_scope)
VALUES (?,?,?,'machine','publisher','controller',?,'delegate',1,?,'run','upload',?,?,'publisher','gh:1',?)`, c.Prefix, hex.EncodeToString(c.Digest[:]), store.DefaultTeam, grants, body, []byte(`[]`), now.UnixNano(), []byte(`[]`))
	if err != nil {
		return store.UploadReservationRequest{}, err
	}
	identity := blob.UploadIdentity{Kind: blob.UploadPublication, Team: c.Team, RunID: "upload", Principal: "publisher", CapabilityRef: c.Prefix}
	return store.UploadReservationRequest{ID: "committed", Intent: blob.UploadIntent{Authority: blob.Authority{CapabilityRef: c.Prefix}, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: now.Add(time.Hour)}, nil
}
