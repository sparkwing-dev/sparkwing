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

// ClaimTeamHeader names the team a claim token's claim belongs to on the
// controller's answer to a log-claim validation.
const ClaimTeamHeader = "X-Sparkwing-Claim-Team"

// MaxClaimTokenLifetime bounds a claim token's hard expiry, which is the
// deadline of the Job that carries it.
const MaxClaimTokenLifetime = 6 * time.Hour

// ClaimTokenKind names what a claim token's claim runs.
type ClaimTokenKind string

const (
	ClaimTokenPlan ClaimTokenKind = "plan"
	ClaimTokenWork ClaimTokenKind = "work"
	// ClaimTokenNode is a runner's claim on one node of a run its trigger
	// holder drives; see [Store.AuthorizeRunnerClaim].
	ClaimTokenNode ClaimTokenKind = "node"
	// ClaimTokenTrigger is a runner's claim on a trigger, which the secret
	// route alone answers.
	ClaimTokenTrigger ClaimTokenKind = "trigger"
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
	// ClaimLogTail routes upload and seal the node attempt's log: a live
	// claim authorizes, and so does a launcher's or runner's node claim
	// whose attempt ended less than [NodeLogSealGrace] ago while no later
	// claim holds the node, for that attempt alone.
	ClaimLogTail
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
	// Ended is set only by a [ClaimResult] or [ClaimLogTail]
	// authorization: a result route then replays the committed result
	// instead of committing one, and a log route writes only Attempt.
	Ended bool
	// Attempt is, for an ended [ClaimLogTail] claim, the attempt ordinal
	// whose log it may still finish.
	Attempt int64
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
	return insertClaimTokenTx(ctx, tx, team, runID, nodeID, generation, kind, expiresAt, now)
}

func insertClaimTokenTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, generation int64,
	kind ClaimTokenKind, expiresAt, now time.Time,
) (string, error) {
	live, err := claimLiveTx(ctx, tx, team, runID, nodeID, generation, now)
	if err != nil {
		return "", err
	}
	if !live {
		return "", ErrClaimNotLive
	}
	raw, err := newClaimTokenRaw()
	if err != nil {
		return "", err
	}
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

func newClaimTokenRaw() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return ClaimTokenPrefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
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
	digest := claimTokenDigest(raw)
	tok, err := s.authorizeClaim(ctx, `c.digest = ?`, []any{digest}, class, now)
	if errors.Is(err, errClaimTokenUnknown) {
		return s.authorizeTriggerClaim(ctx, digest, class, now)
	}
	return tok, err
}

var errClaimTokenUnknown = fmt.Errorf("%w: no such token", ErrClaimTokenInvalid)

// CheckClaimSensitive applies [ClaimSensitive] to the claim tok names, for a
// use that outlasts the request that authorized it: a fetch that ran for
// minutes, or a signed grant presented later.
func (s *Store) CheckClaimSensitive(ctx context.Context, tok ClaimToken, now time.Time) (ClaimToken, error) {
	return s.authorizeClaim(ctx, `c.team = ? AND c.run_id = ? AND c.node_id = ? AND c.claim_generation = ?`,
		[]any{string(tok.Team), tok.RunID, tok.NodeID, tok.Generation}, ClaimSensitive, now)
}

func (s *Store) authorizeClaim(ctx context.Context, where string, args []any, class ClaimRouteClass, now time.Time) (ClaimToken, error) {
	var (
		tok            ClaimToken
		kind           string
		team           string
		expires        int64
		status         sql.NullString
		claimedBy      sql.NullString
		lease, nodeGen sql.NullInt64
		claimant       sql.NullString
	)
	err := s.queryRow(ctx, `SELECT c.prefix, c.team, c.run_id, c.node_id, c.claim_generation, c.kind, c.expires_at,
       n.status, n.claimed_by, n.lease_expires_at, n.claim_generation, n.claim_token_prefix
  FROM claim_tokens c
  LEFT JOIN nodes n ON n.team = c.team AND n.run_id = c.run_id AND n.node_id = c.node_id
 WHERE `+where, args...).Scan(
		&tok.Prefix, &team, &tok.RunID, &tok.NodeID, &tok.Generation, &kind, &expires,
		&status, &claimedBy, &lease, &nodeGen, &claimant)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimToken{}, errClaimTokenUnknown
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
	if tok.Kind == ClaimTokenNode && (live || class == ClaimLogTail) {
		if err := s.claimantLive(ctx, claimant.String, now); err != nil {
			return ClaimToken{}, err
		}
	}
	return s.applyClaimClass(ctx, tok, live, class, now)
}

// safety: a runner's claim token acts as the runner's credential confined to
// one claim, so it ends the moment that credential is revoked or expires.
func (s *Store) claimantLive(ctx context.Context, prefix string, now time.Time) error {
	if prefix == "" {
		return fmt.Errorf("%w: the claim names no credential", ErrClaimTokenInvalid)
	}
	var revoked, expires sql.NullInt64
	err := s.queryRow(ctx, `SELECT revoked_at, expires_at FROM tokens WHERE prefix = ?`, prefix).Scan(&revoked, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: the claiming credential is gone", ErrClaimTokenInvalid)
	}
	if err != nil {
		return err
	}
	if err := TokenEnded(nullableUnix(revoked), nullableUnix(expires), now); err != nil {
		return fmt.Errorf("%w: the claiming credential %w", ErrClaimTokenInvalid, err)
	}
	return nil
}

func (s *Store) applyClaimClass(ctx context.Context, tok ClaimToken, live bool, class ClaimRouteClass, now time.Time) (ClaimToken, error) {
	switch class {
	case ClaimResult:
		tok.Ended = !live
		return tok, nil
	case ClaimLogTail:
		if live {
			return tok, nil
		}
		return s.claimLogTail(ctx, tok, now)
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

// safety: the claim's attempt rows name the attempts it ran, so only the first of them, which its writer keeps
// across in-process retries, only for the grace after the later of its last attempt's end and its node's, and only while
// no later claim holds the node, may still finish its log. A launcher's work claim and a runner's node claim end
// when their node reports, before the process that holds the log seals it; no other kind does.
func (s *Store) claimLogTail(ctx context.Context, tok ClaimToken, now time.Time) (ClaimToken, error) {
	if tok.Kind != ClaimTokenWork && tok.Kind != ClaimTokenNode {
		return ClaimToken{}, ErrClaimNotLive
	}
	var (
		first, attemptEnd, nodeEnd sql.NullInt64
		consumed                   int64
		status                     string
	)
	err := s.queryRow(ctx, `SELECT MIN(a.attempt_ordinal), MAX(a.finished_at), n.attempts_consumed, n.status, n.finished_at
  FROM nodes n
  LEFT JOIN node_execution_attempts a ON a.team = n.team AND a.run_id = n.run_id AND a.node_id = n.node_id
   AND a.claim_generation = n.claim_generation
 WHERE n.team = ? AND n.run_id = ? AND n.node_id = ? AND n.claim_generation = ?
 GROUP BY n.attempts_consumed, n.status, n.finished_at`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(&first, &attemptEnd, &consumed, &status, &nodeEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimToken{}, ErrClaimNotLive
	}
	if err != nil {
		return ClaimToken{}, err
	}
	// safety: after-run hooks, cache saves and artifact publication follow the attempt's end and still log, so the
	// grace runs from whichever ended later.
	ended := attemptEnd
	if status == nodeStatusDone && nodeEnd.Valid && (!ended.Valid || nodeEnd.Int64 > ended.Int64) {
		ended = nodeEnd
	}
	if !ended.Valid || now.Sub(time.Unix(0, ended.Int64)) >= NodeLogSealGrace {
		return ClaimToken{}, ErrClaimNotLive
	}
	tok.Attempt, tok.Ended = consumed+1, true
	if first.Valid {
		tok.Attempt = first.Int64
	}
	return tok, nil
}

// safety: a sensitive route that writes calls this inside its writing transaction. The run row is locked
// first, in the store's order (eligibility, trigger, run, node, ledger) that settle and cancel take, so a
// cancel or a lost lease either committed before these reads or waits until the write commits.
func fenceSensitiveClaimTx(ctx context.Context, tx *storeTx, tok ClaimToken, now time.Time) error {
	found, err := lockTeamRunRowTx(ctx, tx, tok.Team, tok.RunID)
	if err != nil {
		return err
	}
	if !found {
		return ErrClaimNotLive
	}
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT expires_at FROM claim_tokens
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND prefix = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, tok.Prefix).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !now.Before(time.Unix(0, expires))) {
		return ErrClaimTokenInvalid
	}
	if err != nil {
		return err
	}
	live, err := claimLiveTx(ctx, tx, tok.Team, tok.RunID, tok.NodeID, tok.Generation, now)
	if err != nil {
		return err
	}
	if !live {
		return ErrClaimNotLive
	}
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return err
	}
	if cancelled {
		return ErrClaimCancelRequested
	}
	return nil
}

// safety: the run's own row decides, and a run with no row reads as cancelled,
// so a run no trigger names can never pass as "not cancelled" through a NULL.
// A cancel request lands on the run for a controller-dispatched run and on its
// trigger for one a trigger holder drives; either one counts.
func claimRunCancelled(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row,
	team Team, runID string,
) (bool, error) {
	var status string
	var runRequested, triggerRequested sql.NullInt64
	err := queryRow(ctx, `SELECT r.status, r.cancel_requested_at, t.cancel_requested_at FROM runs r
  LEFT JOIN triggers t ON t.team = r.team AND t.id = r.id
 WHERE r.team = ? AND r.id = ?`, string(team), runID).Scan(&status, &runRequested, &triggerRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return runRequested.Valid || triggerRequested.Valid ||
		(status != runStatusPending && status != runStatusRunning), nil
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
