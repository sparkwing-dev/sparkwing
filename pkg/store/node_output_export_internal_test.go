package store

import (
	"context"
	"time"
)

// FinishRunAtForTest ends runID with status at finished, so a test can place
// a run past output retention.
func FinishRunAtForTest(ctx context.Context, s *Store, runID, status string, finished time.Time) error {
	_, err := s.exec(ctx, `UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`, status, finished.UnixNano(), runID)
	return err
}
