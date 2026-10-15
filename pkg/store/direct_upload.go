package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// safety: source binding measures its window from commit so upload time cannot consume it.
const sourceBundleRetention = 24 * time.Hour

// Upload describes a legacy committed object.
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
