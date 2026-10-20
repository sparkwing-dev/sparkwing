package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// Snapshot every current membership at sign-in, including teams not yet selected.
// Otherwise a removed membership could first be selected after a later rejoin.
func authenticateSessionTeamsTx(ctx context.Context, tx *storeTx, session string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_membership_epochs(session_hash,team,epoch)
 SELECT s.hash,m.team,COALESCE(e.epoch,0) FROM sessions s
 JOIN memberships m ON m.account_id=s.account_id
 LEFT JOIN membership_epochs e ON e.team=m.team AND e.account_id=m.account_id
 WHERE s.hash=?`, session)
	return err
}

func authenticateSessionTeamTx(ctx context.Context, tx *storeTx, session string, team Team, account string) error {
	epoch, err := membershipEpochTx(ctx, tx, string(team), account)
	if err != nil {
		return err
	}
	// New team creation or invitation acceptance may authorize a team for the first
	// time. Selection can never overwrite a team's original authentication fence.
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,?,?) ON CONFLICT(session_hash,team) DO NOTHING`, session, team, epoch); err != nil {
		return err
	}
	var authenticated int64
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM session_membership_epochs WHERE session_hash=? AND team=?`, session, team).Scan(&authenticated); err != nil {
		return err
	}
	if authenticated != epoch {
		return authorityError(apierr.Unauthenticated, "session membership has ended; sign in again")
	}
	return nil
}
