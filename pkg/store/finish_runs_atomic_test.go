package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFinishRunsIfActiveRollsBackEveryMemberWhenOneUpdateFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	for _, id := range []string{"first", "second"} {
		if err := s.CreateRun(ctx, Run{ID: id, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.exec(ctx, `CREATE TRIGGER fail_second BEFORE UPDATE ON runs WHEN OLD.id = 'second' BEGIN SELECT RAISE(ABORT, 'second member failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunsIfActive(ctx, []string{"first", "second"}, "cancelled", "operator cancel"); err == nil {
		t.Fatal("batch succeeded despite second-member failure")
	}
	for _, id := range []string{"first", "second"} {
		r, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != "running" {
			t.Fatalf("%s status = %q, want running after rollback", id, r.Status)
		}
	}
}
