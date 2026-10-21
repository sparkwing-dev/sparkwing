package store

import (
	"database/sql"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestFinishRunAndUnitRefusesNonterminalTransition(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	finish := FinishRunRequest{Unit: request.Actor.Owner, Transition: RunCompletion{RunID: request.RunID, Cause: RunStart, Actor: request.Actor}}
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
		t.Fatal("nonterminal transition closed the owner unit")
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != RunPending || run.StateSeq != 0 {
		t.Fatalf("refused finish changed run: %+v, %v", run, err)
	}
	var status string
	if err := tenant.s.db.QueryRow(`SELECT status FROM triggers`).Scan(&status); err != nil || status != "claimed" {
		t.Fatalf("refused finish changed owner: %q, %v", status, err)
	}
}

func TestFinishRunAndUnitCommitsTogetherAndReplays(t *testing.T) {
	for _, failure := range []string{"none", "owner_write", "outbox_write"} {
		t.Run(failure, func(t *testing.T) {
			s, _, claim, _ := renewalFixture(t, capability.UnitTrigger)
			rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			tenant := &Tenant{s: s, team: claim.Team}
			actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
			finish := FinishRunRequest{Unit: claim.Owner, Transition: RunCompletion{RunID: claim.RunID, Cause: RunFinish, Actor: actor}}
			if failure == "owner_write" {
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_finish BEFORE UPDATE ON triggers BEGIN SELECT RAISE(ABORT,'owner unavailable'); END`)
			} else if failure == "outbox_write" {
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_finish BEFORE INSERT ON run_effects BEGIN SELECT RAISE(ABORT,'outbox unavailable'); END`)
			}
			if failure != "none" {
				if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
					t.Fatal("finish ignored failed persistence")
				}
				run, err := tenant.ReadRun(t.Context(), claim.RunID)
				if err != nil || run.State != RunRunning || run.FinishedAt != nil || run.StateSeq != 0 {
					t.Fatalf("partial finish: %+v, %v", run, err)
				}
				rewriteSchemaCount(t, s.db, "events", 0)
				rewriteSchemaCount(t, s.db, "run_effects", 0)
				rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_finish`)
			}
			for i := range 2 {
				applied, err := tenant.FinishRunAndUnit(t.Context(), finish)
				if err != nil || applied.Replayed != (i == 1) {
					t.Fatalf("finish %d: %+v, %v", i, applied, err)
				}
			}
			var status string
			var holder, prefix sql.NullString
			var expiry sql.NullInt64
			if err := s.db.QueryRow(`SELECT status,claimed_by,capability_prefix,lease_expires_at FROM triggers`).Scan(&status, &holder, &prefix, &expiry); err != nil || status != "done" || holder.Valid || prefix.String != "" || expiry.Valid {
				t.Fatalf("owner survived finish: %s %v %v %v, %v", status, holder, prefix, expiry, err)
			}
			run, err := tenant.ReadRun(t.Context(), claim.RunID)
			if err != nil || run.State != RunFailed || run.StateSeq != 1 || run.FinishedAt == nil {
				t.Fatalf("finished run: %+v, %v", run, err)
			}
			rewriteSchemaCount(t, s.db, "events", 1)
		})
	}
}
