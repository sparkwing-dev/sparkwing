package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// RunnerClaim is a runner's node claim as the claim token presented for it
// names it: the claim, the credential that made it, and the fence every write
// under the claim carries.
type RunnerClaim struct {
	Token    ClaimToken
	Claimant ClaimIdentity
	Fence    NodeClaimFence
}

// ErrClaimNotRunner refuses [Store.AuthorizeRunnerClaim] a token the launcher
// minted, which answers only the routes written for its claim.
var ErrClaimNotRunner = errors.New("store: the claim token is not a runner's node claim")

// safety: a runner renews its claim for as long as the node runs, so its
// token's only end is the claim's; a hard expiry would cut a long node short.
var runnerClaimTokenExpiry = time.Unix(0, math.MaxInt64)

// safety: only an authenticated claimant gets a token, because the token
// acts as that claimant's credential confined to the claim.
func mintRunnerNodeTokenTx(ctx context.Context, tx *storeTx, claimant ClaimIdentity, team Team, n *Node, now time.Time) error {
	if !claimant.bound() {
		return nil
	}
	raw, err := insertClaimTokenTx(ctx, tx, team, n.RunID, n.NodeID, n.ClaimGeneration,
		ClaimTokenNode, runnerClaimTokenExpiry, now)
	if err != nil {
		return err
	}
	n.ClaimToken = raw
	return nil
}

func mintTriggerClaimTokenTx(ctx context.Context, tx *storeTx, claimant ClaimIdentity, t *Trigger, now time.Time) error {
	if !claimant.bound() {
		return nil
	}
	raw, err := newClaimTokenRaw()
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO trigger_claim_tokens
       (digest, prefix, team, trigger_id, claim_seq, created_at)
SELECT ?, ?, team, id, claim_seq, ? FROM triggers WHERE team = ? AND id = ? AND claim_seq = ?`,
		claimTokenDigest(raw), raw[:PrefixLen], now.UnixNano(), string(NormalizeTeam(t.Team)), t.ID, t.ClaimSeq)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, fmt.Errorf("trigger %s generation %d: no claim to mint a token for", t.ID, t.ClaimSeq))
	}
	t.ClaimToken = raw
	return nil
}

// safety: the trigger row decides liveness on every request, so a released,
// finished, expired or re-claimed trigger refuses its token at once.
func (s *Store) authorizeTriggerClaim(ctx context.Context, digest string, class ClaimRouteClass, now time.Time) (ClaimToken, error) {
	var (
		tok    ClaimToken
		team   string
		status string
		seq    int64
		lease  sql.NullInt64
		holder string
	)
	err := s.queryRow(ctx, `SELECT c.prefix, c.team, c.trigger_id, c.claim_seq, t.status, t.claim_seq, t.lease_expires_at,
       t.claim_token_prefix
  FROM trigger_claim_tokens c
  JOIN triggers t ON t.id = c.trigger_id AND t.team = c.team
 WHERE c.digest = ?`, digest).Scan(&tok.Prefix, &team, &tok.RunID, &tok.Generation, &status, &seq, &lease, &holder)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimToken{}, ErrClaimTokenInvalid
	}
	if err != nil {
		return ClaimToken{}, err
	}
	tok.Team, tok.Kind, tok.ExpiresAt = Team(team), ClaimTokenTrigger, runnerClaimTokenExpiry
	live := status == triggerStatusClaimed && seq == tok.Generation && lease.Valid && lease.Int64 > now.UnixNano()
	if live {
		if err := s.claimantLive(ctx, holder, now); err != nil {
			return ClaimToken{}, err
		}
	}
	return s.applyClaimClass(ctx, tok, live, class, now)
}

// safety: the caller's writing transaction re-reads the trigger under the
// run row's lock, in the store's order, so a release or cancel either
// committed first or waits for the write.
func fenceSensitiveTriggerClaimTx(ctx context.Context, tx *storeTx, tok ClaimToken, now time.Time) error {
	found, err := lockTeamRunRowTx(ctx, tx, tok.Team, tok.RunID)
	if err != nil {
		return err
	}
	if !found {
		return ErrClaimNotLive
	}
	var held int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM trigger_claim_tokens c
  JOIN triggers t ON t.id = c.trigger_id AND t.team = c.team
 WHERE c.team = ? AND c.trigger_id = ? AND c.claim_seq = ? AND c.prefix = ?
   AND t.claim_seq = c.claim_seq AND `+triggerClaimLiveSQL("t."),
		string(tok.Team), tok.RunID, tok.Generation, tok.Prefix, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimNotLive
	}
	if err != nil {
		return err
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

// AuthorizeRunnerClaim resolves raw to the runner node claim it was minted
// for. It reads the token and the node row on every call, so the token stops
// with the claim: a finished node, a lapsed lease or a later claim of the node
// refuses it at once. A launcher's token answers [ErrClaimNotRunner].
func (s *Store) AuthorizeRunnerClaim(ctx context.Context, raw string, now time.Time) (RunnerClaim, error) {
	if !strings.HasPrefix(raw, ClaimTokenPrefix+"_") || len(raw) < PrefixLen {
		return RunnerClaim{}, ErrClaimTokenInvalid
	}
	var (
		rc                          RunnerClaim
		team, kind                  string
		status                      sql.NullString
		claimedBy, principal, owner sql.NullString
		membership, reservation     sql.NullString
		lease, nodeGen              sql.NullInt64
	)
	err := s.queryRow(ctx, `SELECT c.prefix, c.team, c.run_id, c.node_id, c.claim_generation, c.kind,
       n.status, n.claimed_by, n.lease_expires_at, n.claim_generation, n.claim_principal, n.claim_token_prefix,
       n.claim_membership_id, n.reservation_id
  FROM claim_tokens c
  LEFT JOIN nodes n ON n.team = c.team AND n.run_id = c.run_id AND n.node_id = c.node_id
 WHERE c.digest = ?`, claimTokenDigest(raw)).Scan(
		&rc.Token.Prefix, &team, &rc.Token.RunID, &rc.Token.NodeID, &rc.Token.Generation, &kind,
		&status, &claimedBy, &lease, &nodeGen, &principal, &owner, &membership, &reservation)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerClaim{}, errClaimTokenUnknown
	}
	if err != nil {
		return RunnerClaim{}, err
	}
	if ClaimTokenKind(kind) != ClaimTokenNode {
		return RunnerClaim{}, ErrClaimNotRunner
	}
	rc.Token.Team, rc.Token.Kind, rc.Token.ExpiresAt = Team(team), ClaimTokenNode, runnerClaimTokenExpiry
	live := status.Valid && status.String != nodeStatusDone && claimedBy.Valid &&
		lease.Valid && lease.Int64 > now.UnixNano() && nodeGen.Int64 == rc.Token.Generation
	if !live || !owner.Valid || owner.String == "" {
		return RunnerClaim{}, ErrClaimNotLive
	}
	rc.Claimant = ClaimIdentity{Principal: principal.String, TokenPrefix: owner.String}
	rc.Fence = NodeClaimFence{
		Claimant: rc.Claimant, HolderID: claimedBy.String, MembershipID: membership.String,
		ReservationID: reservation.String, ClaimGeneration: rc.Token.Generation,
	}
	return rc, nil
}
