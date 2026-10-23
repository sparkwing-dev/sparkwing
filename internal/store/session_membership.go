package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// Unselected memberships need the same immutable fence as the active team.
func (op *Operator) recordSessionMembershipsTx(ctx context.Context, tx *storeTx, session string) error {
	rows, err := tx.QueryContext(ctx, `SELECT m.team FROM sessions s JOIN memberships m ON m.account_id=s.account_id WHERE s.hash=?`, session)
	if err != nil {
		return err
	}
	teams, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, team := range teams {
		if err := (&Tenant{s: tx.store, team: Team(team)}).recordSessionMembershipTx(ctx, tx, session); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tenant) recordSessionMembershipTx(ctx context.Context, tx *storeTx, session string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_membership_epochs(session_hash,team,epoch)
 SELECT s.hash,m.team,COALESCE(e.epoch,0) FROM sessions s
 JOIN memberships m ON m.account_id=s.account_id
 LEFT JOIN membership_epochs e ON e.team=m.team AND e.account_id=m.account_id
 WHERE m.team=? AND s.hash=? ON CONFLICT(session_hash,team) DO NOTHING`, t.team, session)
	return err
}

func authenticateSessionTeamTx(ctx context.Context, tx *storeTx, session string, team Team, account string) error {
	epoch, err := membershipEpochTx(ctx, tx, string(team), account)
	if err != nil {
		return err
	}
	// New team creation or invitation acceptance may authorize a team for the first
	// time. Selection can never overwrite a team's original authentication fence.
	if err := (&Tenant{s: tx.store, team: team}).recordSessionMembershipTx(ctx, tx, session); err != nil {
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
