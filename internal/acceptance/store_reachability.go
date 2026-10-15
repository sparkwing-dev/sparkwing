package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type storeBoundaryDeleter func(context.Context, blob.Blob) error

func (f storeBoundaryDeleter) DeleteBlob(ctx context.Context, b blob.Blob) error { return f(ctx, b) }

func checkStoreReachabilityBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-reachability.db")
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
	ticket, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	if _, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{TicketID: ticket.ID, Authority: request.Intent.Authority, Verified: ticket.Blob, ReplicaID: "gc-boundary"}); err != nil {
		return err
	}
	if err := seedStoreReachabilityBoundary(ctx, s.DB()); err != nil {
		return err
	}
	operator := s.AsOperator()
	lease, err := operator.AcquireSingletonLease(ctx, "maintenance", "gc-boundary", time.Hour)
	if err != nil {
		return err
	}
	if err := checkStoreGCFences(ctx, operator, lease); err != nil {
		return err
	}
	retained, err := operator.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || retained.Runs != 0 || retained.Records != 1 || retained.PinnedOutputs != 1 {
		return fmt.Errorf("pinned output and expired record retention=%+v: %v", retained, err)
	}
	if n, err := operator.MarkBlobTombstones(ctx, lease, 100); err != nil || n != 2 {
		return fmt.Errorf("unreachable marks=%d, want 2: %v", n, err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	operator = s.AsOperator()
	if err := checkStoreGCFences(ctx, operator, lease); err != nil {
		return err
	}
	calls := 0
	refusal := errors.New("physical deletion unavailable")
	deleter := storeBoundaryDeleter(func(context.Context, blob.Blob) error { calls++; return refusal })
	if n, err := operator.CollectBlobTombstones(ctx, lease, 100, deleter); err != nil || n != 0 || calls != 0 {
		return fmt.Errorf("grace period deleted bytes: removed=%d calls=%d: %v", n, calls, err)
	}
	old := time.Now().Add(-25 * time.Hour).UnixNano()
	if _, err := s.DB().ExecContext(ctx, "UPDATE blob_tombstones SET marked_at=?", old); err != nil {
		return err
	}
	if _, err := operator.CollectBlobTombstones(ctx, lease, 100, deleter); !errors.Is(err, refusal) || calls != 1 {
		return fmt.Errorf("failed deletion lost its error: calls=%d: %v", calls, err)
	}
	if err := checkStoreGCRows(ctx, s.DB(), 7, 2); err != nil {
		return err
	}
	// safety: a committed graph added after marking must win the collector's reachability recheck.
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)", store.DefaultTeam, strings.Repeat("c", 64), strings.Repeat("e", 64)); err != nil {
		return err
	}
	deleter = storeBoundaryDeleter(func(_ context.Context, b blob.Blob) error {
		if b.Team != string(store.DefaultTeam) || b.Digest != blob.Digest(strings.Repeat("f", 64)) || b.Size != 17 {
			return fmt.Errorf("collector deleted a reachable blob: %+v", b)
		}
		return nil
	})
	if n, err := operator.CollectBlobTombstones(ctx, lease, 100, deleter); err != nil || n != 1 {
		return fmt.Errorf("rechecked collection=%d, want 1: %v", n, err)
	}
	if err := checkStoreGCRows(ctx, s.DB(), 6, 0); err != nil {
		return err
	}
	// safety: deleting the seeded uploader also drops its last operational reference and receipt.
	if _, err := s.DB().ExecContext(ctx, "DELETE FROM pipeline_success_pins WHERE team=?", store.DefaultTeam); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "DELETE FROM runs WHERE team=? AND id='upload'", store.DefaultTeam); err != nil {
		return err
	}
	if n, err := operator.MarkBlobTombstones(ctx, lease, 100); err != nil || n != 5 {
		return fmt.Errorf("unreferenced tail marks=%d, want 5: %v", n, err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE blob_tombstones SET marked_at=?", old); err != nil {
		return err
	}
	deleter = storeBoundaryDeleter(func(context.Context, blob.Blob) error { return nil })
	if n, err := operator.CollectBlobTombstones(ctx, lease, 100, deleter); err != nil || n != 5 {
		return fmt.Errorf("unreferenced tail collection=%d, want 5: %v", n, err)
	}
	if err := checkStoreGCRows(ctx, s.DB(), 1, 0); err != nil {
		return err
	}
	if err := expireStoreSourceBoundary(ctx, s, lease, old); err != nil {
		return err
	}
	return checkStoreGCTakeoverBoundary(ctx, s, lease)
}

func expireStoreSourceBoundary(ctx context.Context, s *store.Store, lease store.SingletonLease, old int64) error {
	// safety: source records retain bytes independently of the originating run and object age.
	if _, err := s.DB().ExecContext(ctx, "UPDATE blob_records SET expires_at=? WHERE team=? AND kind='source_bundle'", old, store.DefaultTeam); err != nil {
		return err
	}
	op := s.AsOperator()
	retained, err := op.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || retained.Records != 1 {
		return fmt.Errorf("source record expiry=%+v: %v", retained, err)
	}
	if n, err := op.MarkBlobTombstones(ctx, lease, 100); err != nil || n != 1 {
		return fmt.Errorf("expired source marks=%d, want 1: %v", n, err)
	}
	deleter := storeBoundaryDeleter(func(_ context.Context, b blob.Blob) error {
		if b.Team != string(store.DefaultTeam) || b.Digest != blob.Digest(strings.Repeat("1", 64)) || b.Size != 19 {
			return fmt.Errorf("wrong source deletion: %+v", b)
		}
		return nil
	})
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, deleter); err != nil || n != 0 {
		return fmt.Errorf("source deleted before grace: %d: %v", n, err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE blob_tombstones SET marked_at=? WHERE team=?", old, store.DefaultTeam); err != nil {
		return err
	}
	if n, err := op.CollectBlobTombstones(ctx, lease, 100, deleter); err != nil || n != 1 {
		return fmt.Errorf("expired source collection=%d, want 1: %v", n, err)
	}
	return checkStoreGCRows(ctx, s.DB(), 0, 0)
}

func checkStoreGCRows(ctx context.Context, db *sql.DB, blobs, tombstones int) error {
	for _, check := range []struct {
		table string
		want  int
	}{{"blobs", blobs}, {"blob_tombstones", tombstones}} {
		var got int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+check.table+" WHERE team=?", store.DefaultTeam).Scan(&got); err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("%s=%d, want %d", check.table, got, check.want)
		}
	}
	return nil
}

// safety: graph seeds isolate persistence and retention from manifest verification and the byte backend.
func seedStoreReachabilityBoundary(ctx context.Context, db *sql.DB) error {
	old := time.Now().Add(-120 * 24 * time.Hour).UnixNano()
	for i, letter := range []string{"b", "c", "d", "e", "f", "1"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)", store.DefaultTeam, strings.Repeat(letter, 64), []int64{5, 7, 11, 13, 17, 19}[i], old); err != nil {
			return err
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"UPDATE runs SET status='success',finished_at=?,created_at=? WHERE team=? AND id='upload'", []any{old, old, store.DefaultTeam}},
		{"INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)", []any{[]byte(`90`)}},
		{"INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES (?,'fixture','upload',?)", []any{store.DefaultTeam, old}},
		{`INSERT INTO blob_records(team,kind,scope,name,digest,manifest_digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at,expires_at) VALUES (?,'output','pinned','result',?,?,'upload','gh:1','manual:','publisher','local',?,'publication','upload',0,0,?,?,?)`, []any{store.DefaultTeam, strings.Repeat("b", 64), strings.Repeat("c", 64), []byte(`{}`), []byte(`{}`), old, old}},
		{`INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at,expires_at) VALUES (?,'depcache','expired','modules',?,'gh:1','manual:','publisher','local',?,'publication','upload',0,0,?,?,?)`, []any{store.DefaultTeam, strings.Repeat("e", 64), []byte(`{}`), []byte(`{}`), old, old}},
		{`INSERT INTO blob_records(team,kind,scope,name,digest,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at,expires_at) VALUES (?,'source_bundle','source','working-tree',?,'gh:1','manual:','publisher','local',?,'publication','upload',0,0,?,?,?)`, []any{store.DefaultTeam, strings.Repeat("1", 64), []byte(`{}`), []byte(`{}`), old, time.Now().Add(time.Hour).UnixNano()}},
		{"INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?)", []any{store.DefaultTeam, strings.Repeat("c", 64), strings.Repeat("d", 64)}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}
