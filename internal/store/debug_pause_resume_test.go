package store

import (
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestResumeDebugPauseCurrentAuthorityProperty(t *testing.T) {
	property := func(selector uint8, suffix string) bool {
		tenant, now, _ := liveHolderFixture(t)
		actor := authorityMember(t, tenant.s, "member", RoleEditor)
		principal, err := tenant.s.ResolveAuthority(t.Context(), actor.Authority)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status_detail='paused' WHERE team='a' AND run_id='run'`)
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) VALUES ('a','run','job','manual',?,?)`, now.UnixNano(), now.Add(1).UnixNano())
		authorized := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
		switch selector % 5 {
		case 1:
			authorized.Target.Team = "b"
		case 2:
			authorized.Target.NodeID = "foreign-" + suffix
		case 3:
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM memberships WHERE account_id='member'`)
		case 4:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='reader' WHERE account_id='member'`)
		}
		err = tenant.ResumeAuthorizedDebugPause(t.Context(), authorized)
		allowed := selector%5 == 0
		if (err == nil) != allowed {
			t.Logf("selector=%d err=%v", selector%5, err)
			return false
		}
		var released int
		if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM debug_pauses WHERE released_at IS NOT NULL`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		before, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil {
			t.Fatal(err)
		}
		if before.Status != StatusRunning || before.Generation != 2 || before.Attempt != 3 || before.ClaimedBy != "runner" || before.CapabilityPrefix != "claim" {
			t.Log(fmt.Sprintf("execution fence changed: %+v", before))
			return false
		}
		return (released == 1) == allowed && (before.StatusDetail == "") == allowed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}
