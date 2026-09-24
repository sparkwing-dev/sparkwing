package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DirectUploadMaxSize bounds one presigned PUT and one S3 copy.
const DirectUploadMaxSize int64 = 500 << 20

// DirectUploadTTL matches the bucket's pending/ lifecycle window.
const DirectUploadTTL = 24 * time.Hour

// DirectCacheMaxAge is the controller's retention window for cache keys.
const DirectCacheMaxAge = 30 * 24 * time.Hour

const directUploadTablesSQL = `CREATE TABLE IF NOT EXISTS uploads (
    id TEXT PRIMARY KEY,
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    store TEXT NOT NULL,
    key TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    principal TEXT NOT NULL,
    claim_prefix TEXT NOT NULL,
    provenance TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    committed_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_uploads_expiry ON uploads(expires_at);
CREATE TABLE IF NOT EXISTS data_objects (
    team TEXT NOT NULL,
    key TEXT NOT NULL,
    store TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    principal TEXT NOT NULL,
    provenance TEXT NOT NULL,
    committed_at BIGINT NOT NULL,
    PRIMARY KEY (team, key, provenance)
);
CREATE TABLE IF NOT EXISTS team_build_trust (
    team TEXT PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0
);`

func applyDirectUploadMigration(ctx context.Context, tx *storeTx) error {
	return execStatements(ctx, tx, directUploadTablesSQL)
}

// UploadRequest declares an exact object before the client sends its bytes.
type UploadRequest struct {
	Team        Team
	RunID       string
	Kind        StorageKind
	Key         string
	Size        int64
	SHA256      string
	Principal   string
	ClaimPrefix string
	Provenance  string
	Now         time.Time
}

// Upload is a reservation and its immutable destination.
type Upload struct {
	ID          string      `json:"upload_id"`
	Team        Team        `json:"team"`
	RunID       string      `json:"run_id"`
	Kind        StorageKind `json:"kind"`
	Key         string      `json:"key"`
	Size        int64       `json:"size"`
	SHA256      string      `json:"sha256"`
	Principal   string      `json:"principal"`
	ClaimPrefix string      `json:"claim_prefix"`
	Provenance  string      `json:"provenance"`
	ExpiresAt   time.Time   `json:"expires_at"`
	CommittedAt time.Time   `json:"committed_at,omitzero"`
}

// ErrObjectExists means an immutable destination is already published.
var ErrObjectExists = errors.New("object already committed")

func validUploadKey(key string) bool {
	if key == "" || len(key) > 900 || strings.HasPrefix(key, "/") {
		return false
	}
	for _, c := range key {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return strings.HasPrefix(key, "bin/") || strings.HasPrefix(key, "artifacts/")
}

func validSHA256(raw string) bool {
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// ReserveUpload reserves the exact declared size and records a pending upload
// in the same transaction. No object is visible through CommittedObject yet.
func (s *Store) ReserveUpload(ctx context.Context, req UploadRequest) (_ Upload, err error) {
	if req.RunID == "" || !validUploadKey(req.Key) || !validSHA256(req.SHA256) || req.Size < 0 || req.Size > DirectUploadMaxSize ||
		req.Principal == "" || (req.Provenance != "local" && req.Provenance != "cloud") {
		return Upload{}, fmt.Errorf("%w: invalid direct upload declaration", ErrInvalidInput)
	}
	req.Team = NormalizeTeam(req.Team)
	if err := checkStorageTeam(req.Team, req.Kind); err != nil {
		return Upload{}, err
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM data_objects WHERE team = ? AND key = ? AND provenance = ?`, string(req.Team), req.Key, req.Provenance).Scan(&exists)
	if err == nil {
		return Upload{}, ErrObjectExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Upload{}, err
	}
	res, err := reserveStorageTx(ctx, tx, StorageReserve{
		Team: req.Team, Kind: req.Kind, Bytes: req.Size, TTL: DirectUploadTTL, Now: req.Now,
	})
	if err != nil {
		return Upload{}, err
	}
	u := Upload{
		ID: res.ID, Team: req.Team, RunID: req.RunID, Kind: req.Kind, Key: req.Key, Size: req.Size,
		SHA256: req.SHA256, Principal: req.Principal, ClaimPrefix: req.ClaimPrefix,
		Provenance: req.Provenance, ExpiresAt: res.ExpiresAt,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO uploads
        (id, team, run_id, store, key, size, sha256, principal, claim_prefix, provenance, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, string(u.Team), u.RunID, string(u.Kind), u.Key, u.Size, u.SHA256, u.Principal, u.ClaimPrefix, u.Provenance, u.ExpiresAt.UnixNano())
	if err != nil {
		return Upload{}, err
	}
	return u, tx.Commit()
}

// UploadForTeam returns a pending or committed upload only to its own team.
func (s *Store) UploadForTeam(ctx context.Context, team Team, id string) (Upload, error) {
	var u Upload
	var kind, teamRaw string
	var expires, committed int64
	err := s.queryRow(ctx, `SELECT id, team, run_id, store, key, size, sha256, principal, claim_prefix, provenance, expires_at, committed_at
        FROM uploads WHERE id = ? AND team = ?`, id, string(NormalizeTeam(team))).Scan(
		&u.ID, &teamRaw, &u.RunID, &kind, &u.Key, &u.Size, &u.SHA256, &u.Principal, &u.ClaimPrefix, &u.Provenance, &expires, &committed)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	u.Team, u.Kind = Team(teamRaw), StorageKind(kind)
	u.ExpiresAt = time.Unix(0, expires)
	if committed > 0 {
		u.CommittedAt = time.Unix(0, committed)
	}
	return u, nil
}

// CommittedObject reports the digest and uploader only after the object was
// copied to its immutable key and its storage transaction committed.
func (s *Store) CommittedObject(ctx context.Context, team Team, key string) (Upload, error) {
	var u Upload
	var kind string
	var committed int64
	err := s.queryRow(ctx, `SELECT store, size, sha256, principal, provenance, committed_at FROM data_objects
        WHERE team = ? AND key = ? ORDER BY CASE provenance WHEN 'cloud' THEN 0 ELSE 1 END LIMIT 1`, string(NormalizeTeam(team)), key).Scan(
		&kind, &u.Size, &u.SHA256, &u.Principal, &u.Provenance, &committed)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	u.Team, u.Kind, u.Key, u.CommittedAt = NormalizeTeam(team), StorageKind(kind), key, time.Unix(0, committed)
	return u, nil
}

// CommittedObjectFor resolves one immutable object using the reader's build trust policy.
func (s *Store) CommittedObjectFor(ctx context.Context, team Team, key string, cloud bool) (Upload, error) {
	trust, err := s.TrustLocalBuilds(ctx, team)
	if err != nil {
		return Upload{}, err
	}
	query := `SELECT store, size, sha256, principal, provenance, committed_at FROM data_objects WHERE team = ? AND key = ?`
	if cloud && !trust {
		query += ` AND provenance = 'cloud'`
	}
	query += ` ORDER BY CASE provenance WHEN 'cloud' THEN 0 ELSE 1 END LIMIT 1`
	var u Upload
	var kind string
	var committed int64
	err = s.queryRow(ctx, query, string(NormalizeTeam(team)), key).Scan(&kind, &u.Size, &u.SHA256, &u.Principal, &u.Provenance, &committed)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	u.Team, u.Kind, u.Key, u.CommittedAt = NormalizeTeam(team), StorageKind(kind), key, time.Unix(0, committed)
	return u, nil
}

// BinaryObject resolves an input hash to the newest content-addressed binary
// this team committed. Cloud readers see only cloud provenance unless trust
// for local builds is on.
func (s *Store) BinaryObject(ctx context.Context, team Team, input string, cloud bool) (Upload, error) {
	if len(input) != 17 || input[8] != '-' {
		return Upload{}, ErrInvalidInput
	}
	for i, c := range input {
		if i != 8 && (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return Upload{}, ErrInvalidInput
		}
	}
	trust, err := s.TrustLocalBuilds(ctx, team)
	if err != nil {
		return Upload{}, err
	}
	query := `SELECT key FROM data_objects WHERE team = ? AND store = ? AND key LIKE ?`
	if cloud && !trust {
		query += ` AND provenance = 'cloud'`
	}
	query += ` ORDER BY committed_at DESC, key DESC LIMIT 1`
	var key string
	err = s.queryRow(ctx, query, string(NormalizeTeam(team)), string(StorageCache), "bin/"+input+"/%").Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	return s.CommittedObject(ctx, team, key)
}

// CommitUpload publishes a verified and copied upload exactly once. The
// recorded uploader comes from the final object's verified S3 metadata.
func (s *Store) CommitUpload(ctx context.Context, team Team, id, recordedUploader string, now time.Time) (err error) {
	if recordedUploader == "" {
		return ErrInvalidInput
	}
	team = NormalizeTeam(team)
	if now.IsZero() {
		now = time.Now()
	}
	u, err := s.UploadForTeam(ctx, team, id)
	if err != nil {
		return err
	}
	if !u.CommittedAt.IsZero() {
		return nil
	}
	if !now.Before(u.ExpiresAt) {
		return ErrNotFound
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if _, _, err := lockTeamStorageTx(ctx, tx, team, u.Kind, now); err != nil {
		return err
	}
	var committed int64
	err = tx.QueryRowContext(ctx, `SELECT committed_at FROM uploads WHERE id = ? AND team = ?`+tx.forUpdate(), id, string(team)).Scan(&committed)
	if err != nil {
		return err
	}
	if committed > 0 {
		return tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO data_objects
        (team, key, store, size, sha256, principal, provenance, committed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (team, key, provenance) DO NOTHING`,
		string(team), u.Key, string(u.Kind), u.Size, u.SHA256, recordedUploader, u.Provenance, now.UnixNano())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrObjectExists
	}
	if err := commitStorageTx(ctx, tx, StorageCommit{ID: id, Team: team, Kind: u.Kind, Bytes: u.Size, Now: now}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE uploads SET committed_at = ? WHERE id = ? AND team = ?`, now.UnixNano(), id, string(team))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// PruneExpiredUploads removes upload records after the pending lifecycle
// window; committed objects remain in data_objects.
func (s *Store) PruneExpiredUploads(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM uploads WHERE expires_at <= ?`, now.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneExpiredCacheObjects removes direct cache rows only after the cache
// bucket's successful retention listing has removed their bytes.
func (s *Store) PruneExpiredCacheObjects(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-DirectCacheMaxAge).UnixNano()
	res, err := s.exec(ctx, `DELETE FROM data_objects WHERE store = ? AND committed_at <= ?`,
		string(StorageCache), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TrustLocalBuilds reports whether the team permits cloud runners to execute
// a locally uploaded binary. The default is false.
func (s *Store) TrustLocalBuilds(ctx context.Context, team Team) (bool, error) {
	var n int64
	err := s.queryRow(ctx, `SELECT trust_local_builds FROM team_build_trust WHERE team = ?`, string(NormalizeTeam(team))).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n != 0, err
}

// SetTrustLocalBuilds changes a team's local binary execution policy.
func (s *Store) SetTrustLocalBuilds(ctx context.Context, team Team, trust bool) error {
	if team == "" {
		return ErrInvalidInput
	}
	n := 0
	if trust {
		n = 1
	}
	_, err := s.exec(ctx, `INSERT INTO team_build_trust (team, trust_local_builds) VALUES (?, ?)
        ON CONFLICT (team) DO UPDATE SET trust_local_builds = excluded.trust_local_builds`,
		string(NormalizeTeam(team)), n)
	return err
}
