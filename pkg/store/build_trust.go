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
