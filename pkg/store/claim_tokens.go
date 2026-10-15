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
	"time"
)

// ClaimTokenPrefix marks a claim token; raw is `swc_<entropy>`.
const ClaimTokenPrefix = "swc"

// ClaimTokenKind names what a claim token's claim runs.
type ClaimTokenKind string

const (
	ClaimTokenPlan ClaimTokenKind = "plan"
	ClaimTokenWork ClaimTokenKind = "work"
	// ClaimTokenNode identifies a runner job claim.
	ClaimTokenNode ClaimTokenKind = "node"
	// ClaimTokenTrigger is a runner's claim on a trigger, which the secret
	// route alone answers.
	ClaimTokenTrigger ClaimTokenKind = "trigger"
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
}

// safety: the raw token carries 256 random bits, so a plain digest is as
// hard to invert as a slow hash and lets every request look the token up
// by primary key instead of paying argon2 or trusting a cache.
func claimTokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
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
