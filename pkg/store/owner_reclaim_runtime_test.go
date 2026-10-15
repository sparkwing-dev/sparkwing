package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerReclaimFencesLostHostAndKeepsTerminalAbsorbing(t *testing.T) {
	s, demand := unitClaimFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	first, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenewUnit(t.Context(), first.Award.CapabilityPrefix, true); err != nil {
		t.Fatal(err)
	}
	now = first.Award.LeaseExpiresAt
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if reaped, err := s.ReapUnits(t.Context(), lease, 1); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("recovery: %+v, %v", reaped, err)
	}
	second, err := s.ClaimUnit(t.Context(), demand)
	if err != nil || second.Award.Generation != first.Award.Generation+1 || second.RunID != first.RunID {
		t.Fatalf("reclaim: %+v, %v", second, err)
	}
	tenant := &Tenant{s: s, team: first.Team}
	finish := func(claim UnitClaim) (Applied, error) {
		actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
		return tenant.FinishRunAndUnit(t.Context(), FinishRunRequest{Unit: claim.Owner, Transition: RunTransition{RunID: claim.RunID, To: RunSuccess, Cause: RunFinish, Actor: actor}})
	}
	if _, err := finish(first); err == nil {
		t.Fatal("lost owner finished the replacement's run")
	}
	if err := s.ReleaseUnit(t.Context(), first, first.Award.CapabilityPrefix); err == nil {
		t.Fatal("lost owner released the replacement's unit")
	}
	if _, err := finish(second); err != nil {
		t.Fatal(err)
	}
	if reaped, err := s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
		t.Fatalf("finished owner recovered: %+v, %v", reaped, err)
	}
	if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("terminal run was claimed again")
	}
	run, err := tenant.ReadRun(t.Context(), first.RunID)
	if err != nil || run.State != RunSuccess || run.FinishedAt == nil {
		t.Fatalf("terminal run: %+v, %v", run, err)
	}
}

func TestOwnerReaperLimitRecoversUnstartedWorkAcrossTeams(t *testing.T) {
	s, _, now := maintenanceStore(t)
	for i, team := range []Team{"a", "b"} {
		id := "run-" + string(team)
		maintenanceRun(t, s, id, i+1, RunPending)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET team=? WHERE id=?`, team, id)
		rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES (?,?,'claimed','trigger',1,1,'owner','owner-cap',7,?,?,?)`, team, id, now.UnixNano(), []byte(`[]`), []byte(`[]`))
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[Team]bool{}
	for range 2 {
		page, err := s.ReapUnits(t.Context(), lease, 1)
		if err != nil || len(page) != 1 || !page[0].Requeued || page[0].Disruptions != 0 || seen[page[0].Team] {
			t.Fatalf("bounded recovery: %+v, %v", page, err)
		}
		seen[page[0].Team] = true
	}
	if page, err := s.ReapUnits(t.Context(), lease, 1); err != nil || len(page) != 0 {
		t.Fatalf("completed recovery: %+v, %v", page, err)
	}
	for team := range seen {
		row, err := (&Tenant{s: s, team: team}).ReadRun(t.Context(), "run-"+string(team))
		if err != nil || row.State != RunPending || row.FinishedAt != nil {
			t.Fatalf("unstarted run: %+v, %v", row, err)
		}
	}
}
