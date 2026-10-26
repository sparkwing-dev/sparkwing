package store

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestCompletionReadersCloseRowsBeforeQueryingClock(t *testing.T) {
	s, _ := singleCursorStore(t)
	db := s.db
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	maintenanceRun(t, s, "run", 1, runstate.RunSuccess)
	for _, effect := range []Effect{EffectProfileFold, effectGitHubCheck, effectMetrics, effectNotifyParent} {
		rewriteSchemaExec(t, db, "INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','run',?,'success',1,1)", effect)
	}
	s.dialect = DialectPostgres
	for _, read := range []struct {
		name string
		fn   func(context.Context, int) ([]DesiredRunEffect, error)
		want int
	}{
		{"all", s.AsOperator().DueRunEffects, 4},
		{"profile", s.AsOperator().DueProfileFolds, 1},
		{"github", s.AsOperator().DueGitHubChecks, 1},
	} {
		t.Run(read.name, func(t *testing.T) {
			due, err := read.fn(t.Context(), 10)
			if err != nil || len(due) != read.want {
				t.Fatalf("due effects=%d, want %d: %v", len(due), read.want, err)
			}
			for _, effect := range due {
				if effect.ObservedAt.IsZero() || !effect.ObservedAt.Equal(due[0].ObservedAt) {
					t.Fatalf("inconsistent observation time: %+v", due)
				}
			}
		})
	}
}
