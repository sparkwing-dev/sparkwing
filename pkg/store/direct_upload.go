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

// DirectUploadMaxPending bounds a team's uncommitted, unexpired uploads.
const DirectUploadMaxPending = 100

// safety: a source stays readable through a queued or active run, then one
// day after it finishes; an unused upload starts its day at commit.
const sourceBundleRetention = 24 * time.Hour

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

// safety: an empty ref marks every object written before v88 and by any
// caller that is not a claim token, which a claim never reads; a claim's
// object carries its run's git ref.
var cacheRefCols = map[string]string{"ref": "TEXT NOT NULL DEFAULT ''", "repo": "TEXT NOT NULL DEFAULT ''"}

func applyCacheRefMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	return addDispatchColumnsTx(ctx, tx, postgres, map[string]map[string]string{
		"uploads": cacheRefCols, "data_objects": cacheRefCols,
	})
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
	// Repo and Ref are the repository and git ref a claim token's run writes
	// under, both taken from the run; empty for every other caller.
	Repo string
	Ref  string
	Now  time.Time
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
	Repo        string      `json:"repo,omitempty"`
	Ref         string      `json:"ref,omitempty"`
	ExpiresAt   time.Time   `json:"expires_at"`
	CommittedAt time.Time   `json:"committed_at,omitzero"`
}

// ErrObjectExists means an immutable destination is already published.
var ErrObjectExists = errors.New("object already committed")

// ErrTooManyPendingUploads refuses a reservation while the team already
// holds [DirectUploadMaxPending] uncommitted uploads.
var ErrTooManyPendingUploads = fmt.Errorf("a team holds at most %d uncommitted uploads; commit them or wait for them to expire", DirectUploadMaxPending)

// ErrSourceAlreadyBound requires a new source upload for another run.
var ErrSourceAlreadyBound = errors.New("source bundle already used or expired; upload again")

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
	return strings.HasPrefix(key, "bin/") || strings.HasPrefix(key, "artifacts/") || strings.HasPrefix(key, "sources/") ||
		strings.HasPrefix(key, "outputs/")
}

func validSHA256(raw string) bool {
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// SourceKeyDigest accepts one submission's immutable source object key.
func SourceKeyDigest(key string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "sources" || !validSHA256(parts[1]) || len(parts[2]) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || strings.ToLower(parts[2]) != parts[2] {
		return "", false
	}
	return parts[1], true
}

// ReserveUpload reserves the exact declared size and records a pending upload
// in the same transaction. No object is visible through CommittedObject yet.
func (s *Store) ReserveUpload(ctx context.Context, req UploadRequest) (_ Upload, err error) {
	source := strings.HasPrefix(req.Key, "sources/")
	output := strings.HasPrefix(req.Key, "outputs/")
	sourceDigest, validSource := SourceKeyDigest(req.Key)
	if (!source && req.RunID == "") ||
		(output && !strings.HasPrefix(req.Key, runOutputPrefix(req.RunID))) ||
		req.Kind != StorageCache ||
		(source && (!validSource || req.RunID != "" || sourceDigest != req.SHA256 || req.Provenance != "local" || req.ClaimPrefix == "")) ||
		!validUploadKey(req.Key) || !validSHA256(req.SHA256) || req.Size <= 0 || req.Size > DirectUploadMaxSize ||
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
	if err := refuseSecondRefBinaryTx(ctx, tx, req.Team, req.Repo, req.Ref, req.Key, req.Provenance); err != nil {
		return Upload{}, err
	}
	res, err := reserveStorageTx(ctx, tx, StorageReserve{
		Team: req.Team, Kind: req.Kind, Bytes: req.Size, TTL: DirectUploadTTL, Now: req.Now, SmallOutput: output,
	})
	if err != nil {
		return Upload{}, err
	}
	if output {
		if err := checkRunOutputRoomTx(ctx, tx, req.Team, req.RunID, req.Size, req.Now); err != nil {
			return Upload{}, err
		}
	}
	// safety: reserveStorageTx holds the team's cache storage row, and every
	// upload is cache, so concurrent reservations count one after another.
	var pending int
	// safety: a released reservation, such as one whose presign failed, frees
	// its slot even though its upload row stays until expiry.
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM uploads u WHERE u.team = ? AND u.committed_at = 0 AND u.expires_at > ?
        AND EXISTS (SELECT 1 FROM storage_reservations r WHERE r.id = u.id)`,
		string(req.Team), req.Now.UnixNano()).Scan(&pending); err != nil {
		return Upload{}, err
	}
	if pending >= DirectUploadMaxPending {
		return Upload{}, ErrTooManyPendingUploads
	}
	u := Upload{
		ID: res.ID, Team: req.Team, RunID: req.RunID, Kind: req.Kind, Key: req.Key, Size: req.Size,
		SHA256: req.SHA256, Principal: req.Principal, ClaimPrefix: req.ClaimPrefix,
		Provenance: req.Provenance, Repo: req.Repo, Ref: req.Ref, ExpiresAt: res.ExpiresAt,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO uploads
        (id, team, run_id, store, key, size, sha256, principal, claim_prefix, provenance, repo, ref, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, string(u.Team), u.RunID, string(u.Kind), u.Key, u.Size, u.SHA256, u.Principal, u.ClaimPrefix, u.Provenance, u.Repo, u.Ref, u.ExpiresAt.UnixNano())
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
	err := s.queryRow(ctx, `SELECT id, team, run_id, store, key, size, sha256, principal, claim_prefix, provenance, repo, ref, expires_at, committed_at
        FROM uploads WHERE id = ? AND team = ?`, id, string(NormalizeTeam(team))).Scan(
		&u.ID, &teamRaw, &u.RunID, &kind, &u.Key, &u.Size, &u.SHA256, &u.Principal, &u.ClaimPrefix, &u.Provenance, &u.Repo, &u.Ref, &expires, &committed)
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

// SourceBoundToRun proves that one committed source was bound to this run.
func (s *Store) SourceBoundToRun(ctx context.Context, team Team, key, runID string) (bool, error) {
	if _, ok := SourceKeyDigest(key); !ok || runID == "" {
		return false, nil
	}
	var found int
	err := s.queryRow(ctx, `SELECT 1 FROM uploads WHERE team = ? AND key = ? AND run_id = ? AND committed_at > 0`,
		string(NormalizeTeam(team)), key, runID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return found == 1, err
}

// ClaimExpiredSourceBundles marks eligible source rows before their S3 delete.
// A binder cannot take an orphan after this claim; an unremoved mark is returned
// on the next pass so a failed S3 deletion can be retried.
func (s *Store) ClaimExpiredSourceBundles(ctx context.Context, now time.Time) (out []Upload, err error) {
	rows, err := s.query(ctx, `UPDATE uploads AS u SET expires_at = 0
        WHERE u.key LIKE 'sources/%' AND u.committed_at > 0
          AND (u.expires_at = 0
            OR (u.run_id = '' AND u.committed_at <= ?)
            OR (u.run_id != '' AND EXISTS (
                SELECT 1 FROM runs r WHERE r.team = u.team AND r.id = u.run_id
                  AND r.status IN ('success','failed','cancelled')
                  AND r.finished_at IS NOT NULL AND r.finished_at <= ?))
            OR (u.run_id != '' AND NOT EXISTS (
                SELECT 1 FROM runs r WHERE r.team = u.team AND r.id = u.run_id)
                AND u.committed_at <= ?))
        RETURNING id, team, key`,
		now.Add(-sourceBundleRetention).UnixNano(), now.Add(-sourceBundleRetention).UnixNano(), now.Add(-sourceBundleRetention).UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var u Upload
		var team string
		if err := rows.Scan(&u.ID, &team, &u.Key); err != nil {
			return nil, err
		}
		u.Team = Team(team)
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteSourceBundleRows releases a source's durable rows only after S3
// confirmed deletion. The next storage listing reconciles its byte count.
func (s *Store) DeleteSourceBundleRows(ctx context.Context, u Upload) (err error) {
	if _, ok := SourceKeyDigest(u.Key); !ok || u.ID == "" {
		return ErrInvalidInput
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if _, err = tx.ExecContext(ctx, `DELETE FROM data_objects WHERE team = ? AND key = ? AND provenance = 'local'`, string(u.Team), u.Key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM uploads WHERE id = ? AND team = ? AND key = ?`, u.ID, string(u.Team), u.Key); err != nil {
		return err
	}
	return tx.Commit()
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

// BinaryObject resolves an input hash to a content-addressed binary this team
// committed for repo under the first of refs that holds one, newest first
// within a ref. A reader that is not a claim token passes the empty repo and
// the empty ref alone. Cloud readers see only cloud provenance unless trust for
// local builds is on.
func (s *Store) BinaryObject(ctx context.Context, team Team, input string, cloud bool, repo string, refs []string) (Upload, error) {
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
	query := `SELECT key FROM data_objects WHERE team = ? AND store = ? AND key LIKE ? AND repo = ? AND ref = ?`
	if cloud && !trust {
		query += ` AND provenance = 'cloud'`
	}
	query += ` ORDER BY committed_at DESC, key DESC LIMIT 1`
	for _, ref := range refs {
		var key string
		err = s.queryRow(ctx, query, string(NormalizeTeam(team)), string(StorageCache), "bin/"+input+"/%", repo, ref).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Upload{}, err
		}
		return s.CommittedObjectFor(ctx, team, key, cloud)
	}
	return Upload{}, ErrNotFound
}

// safety: a repository's ref holds one binary per input, the first committed,
// so a later build under the same ref cannot replace what its runs execute.
func refuseSecondRefBinaryTx(ctx context.Context, tx *storeTx, team Team, repo, ref, key, provenance string) error {
	input, _, ok := strings.Cut(strings.TrimPrefix(key, "bin/"), "/")
	if ref == "" || !strings.HasPrefix(key, "bin/") || !ok {
		return nil
	}
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM data_objects WHERE team = ? AND repo = ? AND ref = ? AND provenance = ? AND key LIKE ? LIMIT 1`,
		string(team), repo, ref, provenance, "bin/"+input+"/%").Scan(&exists)
	if err == nil {
		return ErrObjectExists
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
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
	if err := takeStorageSlotTx(ctx, tx, team, u.Size, now); err != nil {
		if small, serr := smallOutputWithoutSlotTx(ctx, tx, team, u); serr != nil || !small {
			return errors.Join(err, serr)
		}
	}
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
	if err := refuseSecondRefBinaryTx(ctx, tx, team, u.Repo, u.Ref, u.Key, u.Provenance); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO data_objects
        (team, key, store, size, sha256, principal, provenance, repo, ref, committed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (team, key, provenance) DO NOTHING`,
		string(team), u.Key, string(u.Kind), u.Size, u.SHA256, recordedUploader, u.Provenance, u.Repo, u.Ref, now.UnixNano())
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
	if replayed, err := commitStorageTx(ctx, tx, StorageCommit{ID: id, Team: team, Kind: u.Kind, Bytes: u.Size, Now: now}); err != nil {
		return err
	} else if replayed {
		return ErrInvalidInput
	}
	_, err = tx.ExecContext(ctx, `UPDATE uploads SET committed_at = ? WHERE id = ? AND team = ?`, now.UnixNano(), id, string(team))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// safety: a small output needs no slot while the team's small outputs stay
// within the overage its reservation was granted under, so the commit keeps
// what the reservation promised.
func smallOutputWithoutSlotTx(ctx context.Context, tx *storeTx, team Team, u Upload) (bool, error) {
	if !strings.HasPrefix(u.Key, "outputs/") || u.Size > MaxUnpaidOutputBytes {
		return false, nil
	}
	var used int64
	err := tx.QueryRowContext(ctx, `SELECT used_bytes FROM team_storage WHERE team = ? AND store = ?`,
		string(team), string(u.Kind)).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return used+u.Size <= MaxSmallOutputOverage, nil
}

// PruneExpiredUploads removes upload records after the pending lifecycle
// window; committed objects remain in data_objects.
func (s *Store) PruneExpiredUploads(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM uploads WHERE expires_at <= ? AND (key NOT LIKE 'sources/%' OR committed_at = 0)`, now.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneExpiredCacheObjects removes direct cache rows only after the cache
// bucket's successful retention listing has removed their bytes. Outputs
// expire with their run instead; see [Store.ExpiredOutputRuns].
func (s *Store) PruneExpiredCacheObjects(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-DirectCacheMaxAge).UnixNano()
	res, err := s.exec(ctx, `DELETE FROM data_objects WHERE store = ? AND key NOT LIKE 'sources/%' AND key NOT LIKE 'outputs/%' AND committed_at <= ?`,
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

// TriggerRepoID returns the GitHub repository ID team's trigger id recorded,
// or 0 when it recorded none.
func (s *Store) TriggerRepoID(ctx context.Context, team Team, id string) (int64, error) {
	var repoID int64
	err := s.queryRow(ctx, `SELECT COALESCE(github_repo_id, 0) FROM triggers WHERE team = ? AND id = ?`,
		string(NormalizeTeam(team)), id).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return repoID, err
}
