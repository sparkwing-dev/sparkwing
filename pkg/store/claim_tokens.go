package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClaimTokenPrefix marks a claim token; raw is `swc_<entropy>`.
const ClaimTokenPrefix = "swc"

// MaxClaimTokenLifetime bounds a claim token's hard expiry, which is the
// deadline of the Job that carries it.
const MaxClaimTokenLifetime = 6 * time.Hour

// ClaimTokenKind names what a claim token's claim runs.
type ClaimTokenKind string

const (
	ClaimTokenPlan ClaimTokenKind = "plan"
	ClaimTokenWork ClaimTokenKind = "work"
)

// ClaimRouteClass is the authority a route asks of a claim token.
type ClaimRouteClass int

const (
	// ClaimSensitive routes hand out data: the claim must be live at the
	// token's generation with no cancel requested on its run.
	ClaimSensitive ClaimRouteClass = iota + 1
	// ClaimReporting routes accept state from the pod and unlock nothing:
	// the claim must not have ended, and a cancel request is no bar.
	ClaimReporting
	// ClaimResult routes commit the claim's one result and answer with a
	// status only; an ended claim still authorizes, to replay it.
	ClaimResult
)

var (
	// ErrClaimTokenInvalid refuses a token that is unknown, malformed or
	// past its hard expiry.
	ErrClaimTokenInvalid = errors.New("store: invalid or expired claim token")
	// ErrClaimCancelRequested refuses a sensitive request once the claim's
	// run has a cancel request.
	ErrClaimCancelRequested = errors.New("store: the run this claim belongs to is being cancelled")
	// ErrClaimResultConflict refuses a result that differs from the one
	// the claim committed, or that arrives after the claim ended without
	// committing one.
	ErrClaimResultConflict = errors.New("store: the claim committed a different result or none")
)

// ClaimToken is the claim a presented token is bound to.
type ClaimToken struct {
	Prefix     string
	Team       Team
	RunID      string
	NodeID     string
	Generation int64
	Kind       ClaimTokenKind
	ExpiresAt  time.Time
	// Ended is set only by a [ClaimResult] authorization, whose route then
	// replays the committed result instead of committing one.
	Ended bool
}

const claimTokensTable = `CREATE TABLE IF NOT EXISTS claim_tokens (
    digest           TEXT PRIMARY KEY,
    prefix           TEXT NOT NULL,
    team             TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    node_id          TEXT NOT NULL,
    claim_generation INTEGER NOT NULL,
    kind             TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    result_digest    TEXT NOT NULL DEFAULT '',
    UNIQUE (team, run_id, node_id, claim_generation),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
)`

func applyClaimTokensMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	ddl := claimTokensTable
	if postgres {
		ddl = strings.ReplaceAll(ddl, "INTEGER", "BIGINT")
	}
	_, err := tx.ExecContext(ctx, ddl)
	return err
}

// safety: the raw token carries 256 random bits, so a plain digest is as
// hard to invert as a slow hash and lets every request look the token up
// by primary key instead of paying argon2 or trusting a cache.
func claimTokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// MintClaimToken issues the token for the live claim team holds on
// runID/nodeID at generation, valid until expiresAt at the latest. The raw
// token is returned once; only its digest is stored. It refuses a claim
// that is not live at that generation, and a second token for one claim.
func (s *Store) MintClaimToken(ctx context.Context, team Team, runID, nodeID string, generation int64,
	kind ClaimTokenKind, expiresAt, now time.Time,
) (string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackOrLog(tx)
	raw, err := mintClaimTokenTx(ctx, tx, team, runID, nodeID, generation, kind, expiresAt, now)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return raw, nil
}

func mintClaimTokenTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, generation int64,
	kind ClaimTokenKind, expiresAt, now time.Time,
) (string, error) {
	if kind != ClaimTokenPlan && kind != ClaimTokenWork {
		return "", fmt.Errorf("claim token kind %q is neither %q nor %q", kind, ClaimTokenPlan, ClaimTokenWork)
	}
	if !expiresAt.After(now) || expiresAt.Sub(now) > MaxClaimTokenLifetime {
		return "", fmt.Errorf("claim token expiry must fall within %s of now", MaxClaimTokenLifetime)
	}
	live, err := claimLiveTx(ctx, tx, team, runID, nodeID, generation, now)
	if err != nil {
		return "", err
	}
	if !live {
		return "", ErrClaimNotLive
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	raw := ClaimTokenPrefix + "_" + base64.RawURLEncoding.EncodeToString(buf)
	if _, err := tx.ExecContext(ctx, `INSERT INTO claim_tokens
       (digest, prefix, team, run_id, node_id, claim_generation, kind, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		claimTokenDigest(raw), raw[:PrefixLen], string(team), runID, nodeID, generation, string(kind),
		now.UnixNano(), expiresAt.UnixNano()); err != nil {
		if isUniqueViolation(err) {
			return "", fmt.Errorf("claim %s/%s generation %d already has a token: %w", runID, nodeID, generation, ErrLockHeld)
		}
		return "", err
	}
	return raw, nil
}

func claimLiveTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, generation int64, now time.Time) (bool, error) {
	var held int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?
   AND `+nodeNotDone+` AND `+nodeClaimLiveSQL("")+tx.forUpdate(),
		string(team), runID, nodeID, generation, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AuthorizeClaimToken resolves raw to its claim and applies class. It reads
// the token and the claim's node row on every call, so a lost, finished,
// superseded or cancelled claim is refused on its very next request, and
// every lease extension renews the token up to its hard expiry.
func (s *Store) AuthorizeClaimToken(ctx context.Context, raw string, class ClaimRouteClass, now time.Time) (ClaimToken, error) {
	if !strings.HasPrefix(raw, ClaimTokenPrefix+"_") || len(raw) < PrefixLen {
		return ClaimToken{}, ErrClaimTokenInvalid
	}
	var (
		tok            ClaimToken
		kind           string
		team           string
		expires        int64
		status         sql.NullString
		claimedBy      sql.NullString
		lease, nodeGen sql.NullInt64
	)
	err := s.queryRow(ctx, `SELECT c.prefix, c.team, c.run_id, c.node_id, c.claim_generation, c.kind, c.expires_at,
       n.status, n.claimed_by, n.lease_expires_at, n.claim_generation
  FROM claim_tokens c
  LEFT JOIN nodes n ON n.team = c.team AND n.run_id = c.run_id AND n.node_id = c.node_id
 WHERE c.digest = ?`, claimTokenDigest(raw)).Scan(
		&tok.Prefix, &team, &tok.RunID, &tok.NodeID, &tok.Generation, &kind, &expires,
		&status, &claimedBy, &lease, &nodeGen)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimToken{}, ErrClaimTokenInvalid
	}
	if err != nil {
		return ClaimToken{}, err
	}
	tok.Team, tok.Kind, tok.ExpiresAt = Team(team), ClaimTokenKind(kind), time.Unix(0, expires)
	if !now.Before(tok.ExpiresAt) {
		return ClaimToken{}, ErrClaimTokenInvalid
	}
	live := status.Valid && status.String != nodeStatusDone && claimedBy.Valid &&
		lease.Valid && lease.Int64 > now.UnixNano() && nodeGen.Int64 == tok.Generation
	switch class {
	case ClaimResult:
		tok.Ended = !live
		return tok, nil
	case ClaimReporting, ClaimSensitive:
		if !live {
			return ClaimToken{}, ErrClaimNotLive
		}
		if class == ClaimSensitive {
			cancelled, err := claimRunCancelled(ctx, s.queryRow, tok.Team, tok.RunID)
			if err != nil {
				return ClaimToken{}, err
			}
			if cancelled {
				return ClaimToken{}, ErrClaimCancelRequested
			}
		}
		return tok, nil
	default:
		return ClaimToken{}, fmt.Errorf("unknown claim route class %d", class)
	}
}

// safety: the run's own row decides, and a run with no row reads as cancelled,
// so a run no trigger names can never pass as "not cancelled" through a NULL.
// Schema 77 moves the request onto runs and nodes; this is the one read to switch.
func claimRunCancelled(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row,
	team Team, runID string,
) (bool, error) {
	var status string
	var requested sql.NullInt64
	err := queryRow(ctx, `SELECT r.status, t.cancel_requested_at FROM runs r
  LEFT JOIN triggers t ON t.team = r.team AND t.id = r.id
 WHERE r.team = ? AND r.id = ?`, string(team), runID).Scan(&status, &requested)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return requested.Valid || (status != runStatusPending && status != runStatusRunning), nil
}

// safety: authorization reads the claim before the handler runs, so a store
// function writing on a sensitive route's behalf calls this in its own
// transaction; it locks the node row and refuses a claim lost or cancelled since.
func assertClaimSensitiveTx(ctx context.Context, tx *storeTx, tok ClaimToken, now time.Time) error {
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return err
	}
	if cancelled {
		return ErrClaimCancelRequested
	}
	live, err := claimLiveTx(ctx, tx, tok.Team, tok.RunID, tok.NodeID, tok.Generation, now)
	if err != nil {
		return err
	}
	if !live {
		return ErrClaimNotLive
	}
	return nil
}

// ClaimResultCommit is the result a live claim is about to commit, identified
// by the digest of the request that carries it. A store function that writes a
// claim's result takes one and commits it in the same transaction as its
// writes, so no result lands outside the claim's one-commit fence.
type ClaimResultCommit struct {
	token  ClaimToken
	digest string
}

// NewClaimResultCommit binds digest to tok's claim.
func NewClaimResultCommit(tok ClaimToken, digest string) ClaimResultCommit {
	return ClaimResultCommit{token: tok, digest: digest}
}

// Token returns the claim the result belongs to.
func (c ClaimResultCommit) Token() ClaimToken { return c.token }

// ReplayClaimResult answers a result request whose claim has ended, and
// writes nothing: nil when the claim committed exactly digest, and
// [ErrClaimResultConflict] for any other digest or when it committed none.
func (s *Store) ReplayClaimResult(ctx context.Context, tok ClaimToken, digest string) error {
	var committed string
	err := s.queryRow(ctx, `SELECT result_digest FROM claim_tokens
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(&committed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimTokenInvalid
	}
	if err != nil {
		return err
	}
	if committed == "" || committed != digest {
		return ErrClaimResultConflict
	}
	return nil
}

// safety: a claim commits one result. The caller writes it only on (false, nil);
// the same digest again replays (true, nil) and writes nothing, and a differing
// digest, or any after the claim ended uncommitted, is ErrClaimResultConflict.
func (c ClaimResultCommit) commitTx(ctx context.Context, tx *storeTx, now time.Time) (bool, error) {
	tok, digest := c.token, c.digest
	if digest == "" {
		return false, errors.New("claim result digest is empty")
	}
	live, err := claimLiveTx(ctx, tx, tok.Team, tok.RunID, tok.NodeID, tok.Generation, now)
	if err != nil {
		return false, err
	}
	var committed string
	err = tx.QueryRowContext(ctx, `SELECT result_digest FROM claim_tokens
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`+tx.forUpdate(),
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(&committed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrClaimTokenInvalid
	}
	if err != nil {
		return false, err
	}
	switch {
	case committed == digest:
		return true, nil
	case committed != "" || !live:
		return false, ErrClaimResultConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE claim_tokens SET result_digest = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`,
		digest, string(tok.Team), tok.RunID, tok.NodeID, tok.Generation)
	return false, err
}
