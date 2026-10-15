package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TrustLocalBuilds reports whether this team permits execution of local binaries.
func (t *Tenant) TrustLocalBuilds(ctx context.Context) (bool, error) {
	var trust int64
	err := t.s.queryRow(ctx, "SELECT trust_local_builds FROM team_build_trust WHERE team=?", t.team).Scan(&trust)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if trust != 0 && trust != 1 {
		return false, fmt.Errorf("%w: invalid local binary trust policy", ErrInvalidInput)
	}
	return trust == 1, nil
}

// SetTrustLocalBuilds changes this team's local binary execution policy.
func (t *Tenant) SetTrustLocalBuilds(ctx context.Context, trust bool) error {
	_, err := t.s.exec(ctx, `INSERT INTO team_build_trust(team,trust_local_builds) VALUES (?,?)
ON CONFLICT(team) DO UPDATE SET trust_local_builds=excluded.trust_local_builds`, t.team, boolInt(trust))
	return err
}
