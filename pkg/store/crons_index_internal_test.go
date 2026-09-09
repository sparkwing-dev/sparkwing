package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A store that already carries schedule_name never enters the v33 rebuild, so
// the widened key has to be created outside that branch or such a store keeps
// only the v32 one.
func TestNamedCronsMigrationCreatesTheWidenedIndexWithoutRebuilding(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`DROP INDEX idx_cron_schedules_repo_pipeline_name`); err != nil {
		t.Fatalf("drop the widened index: %v", err)
	}
	if got := countCronNameIndex(ctx, t, s); got != 0 {
		t.Fatalf("index count after the drop = %d, want 0", got)
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyNamedCronsMigrationSQLite(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("re-apply v33: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countCronNameIndex(ctx, t, s); got != 1 {
		t.Errorf("index count after v33 = %d, want 1", got)
	}
}

func TestCronBranchMigrationCreatesTheWidenedIndex(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`DROP INDEX idx_cron_schedules_repo_pipeline_name`); err != nil {
		t.Fatalf("drop the widened index: %v", err)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCronBranchMigrationSQLite(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v34: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countCronNameIndex(ctx, t, s); got != 1 {
		t.Errorf("index count after v34 = %d, want 1", got)
	}
}

func countCronNameIndex(ctx context.Context, t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master
          WHERE type = 'index' AND name = 'idx_cron_schedules_repo_pipeline_name'`).Scan(&n); err != nil {
		t.Fatalf("inspect the widened index: %v", err)
	}
	return n
}
