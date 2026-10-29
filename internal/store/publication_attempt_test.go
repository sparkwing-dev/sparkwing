package store

import (
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestPublicationImportsTerminalAttemptWithoutExecution(t *testing.T) {
	tenant, now, terminal := publicationFixture(t)
	attempt := runspec.PublishedAttempt{Attempt: 2, Generation: 3, StartedAt: now.Add(-time.Second), FinishedAt: *now, Outcome: "success", Executor: "desktop"}
	change := nodeTransition{Ref: terminal.Ref, Actor: terminal.Actor, To: nodePending, Cause: runstate.CausePublishAttempt, Extra: transitionExtra{Publication: &attempt}}
	for i := range 2 {
		applied, err := tenant.TransitionNode(t.Context(), change)
		if err != nil || applied.Replayed != (i == 1) || applied.Attempt != 2 || applied.Generation != 3 {
			t.Fatalf("import %d: %+v %v", i, applied, err)
		}
	}
	if _, err := tenant.TransitionNode(t.Context(), terminal); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), terminal.Ref)
	if err != nil || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.Attempt != 2 || row.Generation != 3 || row.Status != runstate.StatusDone {
		t.Fatalf("imported node: %+v %v", row, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "node_attempts", 1)
	rewriteSchemaCount(t, tenant.s.db, "log_streams", 1)
	var executor string
	if err := tenant.s.db.QueryRow("SELECT executor_name FROM node_attempts WHERE team=? AND run_id=?", tenant.team, terminal.Ref.RunID).Scan(&executor); err != nil {
		t.Fatal(err)
	}
	if executor != "local" {
		t.Fatalf("publication trusted caller executor name: %q", executor)
	}
	rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
}

func TestPublicationAttemptNeverCreatesAnExecutableAward(t *testing.T) {
	property := func(kind uint8, value int64) bool {
		tenant, now, terminal := publicationFixture(t)
		a := runspec.PublishedAttempt{Attempt: 1, Generation: 1, StartedAt: now.Add(-time.Second), FinishedAt: *now, Outcome: "success", Executor: "desktop"}
		change := nodeTransition{Ref: terminal.Ref, Actor: terminal.Actor, To: nodePending, Cause: runstate.CausePublishAttempt, Extra: transitionExtra{Publication: &a}}
		switch kind % 6 {
		case 0:
			a.Attempt = -(value & 0x7fffffff)
		case 1:
			a.Generation = -(value & 0x7fffffff)
		case 2:
			a.FinishedAt = a.StartedAt.Add(-time.Second)
		case 3:
			a.Outcome = "running"
		case 4:
			change.Cause = runstate.CauseStart
		case 5:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET intake_mode='execute' WHERE id=?`, terminal.Ref.RunID)
		}
		_, err := tenant.TransitionNode(t.Context(), change)
		var count int
		if e := tenant.s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM node_attempts`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		return err != nil && count == 0
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 32})); err != nil {
		t.Fatal(err)
	}
}
