package teststore

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SeedRunFinish sets terminal state for fixtures that do not exercise run execution.
func SeedRunFinish(s *store.Store, ctx context.Context, team store.Team, id, status, message string) error {
	query := `UPDATE runs SET status=?,error=?,finished_at=? WHERE team=? AND id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE runs SET status=$1,error=$2,finished_at=$3 WHERE team=$4 AND id=$5`
	}
	result, err := s.DB().ExecContext(ctx, query, status, message, time.Now().UnixNano(), team, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("fixture run %s/%s: matched %d rows", team, id, n)
	}
	return nil
}
