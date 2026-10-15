package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

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
