package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

type sourceGCFixture struct {
	s         *store.Store
	tenant    *store.Tenant
	authority capability.AuthorityRef
	digest    blob.Digest
	lease     store.SingletonLease
}

func newSourceGCFixture(t *testing.T, ctx context.Context) sourceGCFixture {
	t.Helper()
	s := storetest.NewPostgres(t).Open(t)
	s.DB().SetMaxOpenConns(8)
	member, err := s.ResolveSignIn(ctx, googleProfile("uploader", "uploader@example.test"), store.SignUpConditions{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := s.CreateAccountSession(ctx, member.Account, member.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session := sha256.Sum256([]byte(raw))
	tenant, err := s.ForTeam(ctx, member.PersonalTeam)
	if err != nil {
		t.Fatal(err)
	}
	f := sourceGCFixture{s: s, tenant: tenant, authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(session[:])}, digest: blob.Digest(strings.Repeat("d", 64))}
	now, err := s.TestOnlyDatabaseTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, ctx, `INSERT INTO blobs(team,digest,size,created_at) VALUES ($1,$2,3,1)`, tenant.Team(), f.digest)
	f.exec(t, ctx, `INSERT INTO blob_tombstones(team,digest,marked_at) VALUES ($1,$2,$3)`, tenant.Team(), f.digest, now.Add(-limits.BlobGCGrace-time.Minute).UnixNano())
	if f.lease, err = s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "collector", time.Hour); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f sourceGCFixture) exec(t *testing.T, ctx context.Context, query string, args ...any) {
	t.Helper()
	if _, err := f.s.DB().ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f sourceGCFixture) reserve(ctx context.Context, id string) (store.SourceUpload, error) {
	return f.tenant.ReserveSourceUpload(ctx, f.authority, store.SourceUpload{ID: id, Scope: store.SourceUploadScope{Repository: "gh:1", ExecutionSHA: strings.Repeat("a", 40), PipelineSHA: strings.Repeat("a", 40)}, Blob: blob.Blob{Digest: f.digest, Size: 3}, ExpiresAt: time.Now().Add(time.Hour)})
}

func (f sourceGCFixture) blockedBehind(t *testing.T, ctx context.Context, blocker *sql.Tx, done <-chan error) {
	t.Helper()
	var pid int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("store call finished before the blocker released: %v", err)
		default:
		}
		if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
}

func lockBlobDigest(t *testing.T, ctx context.Context, f sourceGCFixture) *sql.Tx {
	t.Helper()
	blocker, err := f.s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Rollback() })
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "blob:"+string(f.tenant.Team())+":"+string(f.digest)); err != nil {
		t.Fatal(err)
	}
	return blocker
}

func requireConflict(t *testing.T, what string, err error) {
	t.Helper()
	var api *apierr.Error
	if !errors.As(err, &api) || api.Code != apierr.Conflict {
		t.Fatalf("%s: want conflict, got %v", what, err)
	}
}

func TestPostgresSourceReservationWaitsForCollectionDecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newSourceGCFixture(t, ctx)
	blocker := lockBlobDigest(t, ctx, f)
	done := make(chan error, 1)
	go func() { _, err := f.reserve(ctx, "racing"); done <- err }()
	f.blockedBehind(t, ctx, blocker, done)
	if _, err := blocker.ExecContext(ctx, `SELECT 1 FROM quota_admission WHERE team=$1 FOR UPDATE NOWAIT`, f.tenant.Team()); err != nil {
		t.Fatalf("reservation locked quota before the digest: %v", err)
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE blob_tombstones SET collecting_at=1 WHERE team=$1 AND digest=$2`, f.tenant.Team(), f.digest); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	requireConflict(t, "reservation behind a collection", <-done)
}

func TestPostgresSourceCommitLocksDigestBeforeQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newSourceGCFixture(t, ctx)
	u, err := f.reserve(ctx, "committing")
	if err != nil {
		t.Fatal(err)
	}
	blocker := lockBlobDigest(t, ctx, f)
	done := make(chan error, 1)
	go func() {
		_, err := f.tenant.CommitSourceUpload(ctx, f.authority, u.ID, blob.Blob{Team: string(f.tenant.Team()), Digest: f.digest, Size: 3}, "replica")
		done <- err
	}()
	f.blockedBehind(t, ctx, blocker, done)
	for _, query := range []string{
		`SELECT 1 FROM quota_admission WHERE team=$1 FOR UPDATE NOWAIT`,
		`SELECT 1 FROM quota_reservations WHERE team=$1 FOR UPDATE NOWAIT`,
	} {
		if _, err := blocker.ExecContext(ctx, query, f.tenant.Team()); err != nil {
			t.Fatalf("commit locked quota before the digest: %v", err)
		}
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBlobCollectionFencesSourceReservationDuringDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newSourceGCFixture(t, ctx)
	var during error
	deleter := sourceGCDeleter(func(ctx context.Context, b blob.Blob) error {
		_, during = f.reserve(ctx, "during")
		return nil
	})
	removed, err := f.s.AsOperator().CollectBlobTombstones(ctx, f.lease, 10, deleter)
	if err != nil || removed != 1 {
		t.Fatalf("collection: removed=%d: %v", removed, err)
	}
	requireConflict(t, "reservation during deletion", during)
	if _, err := f.reserve(ctx, "after"); err != nil {
		t.Fatalf("reservation after collection: %v", err)
	}
	var blobs int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE team=$1 AND digest=$2`, f.tenant.Team(), f.digest).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("collected blob row survived: %d: %v", blobs, err)
	}
}

type sourceGCDeleter func(context.Context, blob.Blob) error

func (d sourceGCDeleter) DeleteBlob(ctx context.Context, b blob.Blob) error { return d(ctx, b) }
