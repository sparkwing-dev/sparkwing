package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRunControlCommitsOnlyForCurrentMemberAuthorityProperty(t *testing.T) {
	property := func(selector uint8, suffix uint64) bool {
		tenant, ref, _, _, _ := approvalReaperFixture(t, "fail")
		actor := authorityMember(t, tenant.s, "member", RoleEditor)
		p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
		if err != nil {
			t.Fatal(err)
		}
		a := capability.Authorized{Principal: p, Target: capability.Target{Team: string(tenant.team), RunID: string(ref.RunID)}}
		switch selector % 6 {
		case 1:
			a.Target.Team = "b"
		case 2:
			a.Target.RunID = "missing"
		case 3:
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM memberships WHERE account_id='member'`)
		case 4:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE memberships SET role='reader' WHERE account_id='member'`)
		case 5:
			a.Target.NodeID = fmt.Sprint(suffix)
		}
		err = tenant.CreateAuthorizedDebugPause(t.Context(), a, string(ref.NodeID), "manual", time.Time{}, "pause")
		allowed := selector%6 == 0
		if (err == nil) != allowed {
			t.Logf("selector=%d pause=%v", selector%6, err)
			return false
		}
		rewriteSchemaCount(t, tenant.s.db, "debug_pauses", map[bool]int{true: 1, false: 0}[allowed])
		a.Target.NodeID = string(ref.NodeID)
		if selector%6 == 5 {
			a.Target.NodeID = fmt.Sprint(suffix)
		}
		out, err := tenant.ResolveAuthorizedApproval(t.Context(), a, ApprovalDecision{Resolution: "approved", IdempotencyKey: "decision"})
		if (err == nil) != allowed {
			t.Logf("selector=%d approval=%v", selector%6, err)
			return false
		}
		if allowed && (out.Approval.Approver != "member" || out.Approval.Resolution != "approved") {
			return false
		}
		rewriteSchemaCount(t, tenant.s.db, "run_control_requests", map[bool]int{true: 2, false: 0}[allowed])
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}

func TestRunControlMasksEncodedClassifiedValuesAndReplaysExactlyProperty(t *testing.T) {
	property := func(seed uint64) bool {
		tenant, ref, _, _, _ := approvalReaperFixture(t, "fail")
		actor := authorityMember(t, tenant.s, "member", RoleEditor)
		p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
		if err != nil {
			t.Fatal(err)
		}
		secret := fmt.Sprintf("private-%016x", seed)
		tenant.s.planSecretOpener = fakePlanSecretOpener(func(_ context.Context, _ Team, _ Secret) (string, error) { return secret, nil })
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at) VALUES ('a','KEY','sealed','owner','p',1,0,1,1)`)
		text := "note " + secret + " encoded " + base64.StdEncoding.EncodeToString([]byte(secret)) + " end"
		a := capability.Authorized{Principal: p, Target: capability.Target{Team: string(tenant.team), RunID: string(ref.RunID)}}
		for range 2 {
			if err := tenant.CreateAuthorizedDebugPause(t.Context(), a, string(ref.NodeID), text, time.Time{}, "pause"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tenant.CreateAuthorizedDebugPause(t.Context(), a, string(ref.NodeID), text+"changed", time.Time{}, "pause"); err == nil {
			return false
		}
		a.Target.NodeID = string(ref.NodeID)
		decision := ApprovalDecision{Resolution: "approved", Comment: text, IdempotencyKey: "approval"}
		first, err := tenant.ResolveAuthorizedApproval(t.Context(), a, decision)
		if err != nil {
			t.Fatal(err)
		}
		again, err := tenant.ResolveAuthorizedApproval(t.Context(), a, decision)
		if err != nil || !again.Replayed || !reflect.DeepEqual(first.Approval, again.Approval) {
			t.Logf("replay=%+v %v", again, err)
			return false
		}
		decision.Comment += "changed"
		if _, err := tenant.ResolveAuthorizedApproval(t.Context(), a, decision); err == nil {
			return false
		}
		rows, err := tenant.s.db.Query(`SELECT reason FROM debug_pauses UNION ALL SELECT comment FROM approvals UNION ALL SELECT CAST(payload AS TEXT) FROM events WHERE payload IS NOT NULL`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, secret) || strings.Contains(text, base64.StdEncoding.EncodeToString([]byte(secret))) {
				t.Logf("unmasked public text: %q", text)
				return false
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaCount(t, tenant.s.db, "debug_pauses", 1)
		rewriteSchemaCount(t, tenant.s.db, "run_control_requests", 2)
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 15}); err != nil {
		t.Fatal(err)
	}
}

func TestRunControlRollsBackStateEventsAndReplayTogether(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(fmt.Sprint(approval), func(t *testing.T) {
			tenant, ref, _, _, _ := approvalReaperFixture(t, "fail")
			actor := authorityMember(t, tenant.s, "member", RoleEditor)
			p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			a := capability.Authorized{Principal: p, Target: capability.Target{Team: "a", RunID: string(ref.RunID), NodeID: string(ref.NodeID)}}
			var events int
			if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if approval {
				rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER reject_receipt BEFORE UPDATE ON run_control_requests BEGIN SELECT RAISE(ABORT,'receipt rejected'); END`)
				out, err := tenant.ResolveAuthorizedApproval(t.Context(), a, ApprovalDecision{Resolution: "approved", Comment: "go", IdempotencyKey: "request"})
				if err == nil || out.Approval.RunID != "" {
					t.Fatalf("failed approval returned %+v %v", out, err)
				}
			} else {
				rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER reject_event BEFORE INSERT ON events WHEN NEW.kind='debug.paused' BEGIN SELECT RAISE(ABORT,'event rejected'); END`)
				if err := tenant.CreateAuthorizedDebugPause(t.Context(), a, string(ref.NodeID), "manual", time.Time{}, "request"); err == nil {
					t.Fatal("failed pause committed")
				}
			}
			rewriteSchemaCount(t, tenant.s.db, "run_control_requests", 0)
			rewriteSchemaCount(t, tenant.s.db, "debug_pauses", 0)
			rewriteSchemaCount(t, tenant.s.db, "events", events)
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Status != runstate.StatusApprovalPending || row.StatusDetail != "" {
				t.Fatalf("failed control changed job: %+v %v", row, err)
			}
			gate, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || gate.ResolvedAt != nil {
				t.Fatalf("failed decision changed approval: %+v %v", gate, err)
			}
		})
	}
}

func TestRunControlMasksClassifiedArguments(t *testing.T) {
	for _, classified := range []bool{false, true} {
		t.Run(fmt.Sprint(classified), func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			request.Request.RequestedArgs = map[string]string{"message": "argument-secret"}
			request.Request.SecretArgs = runspec.Optional[[]string]{Set: classified, Value: []string{"message"}}
			admitted, err := tenant.AdmitRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			actor := authorityMember(t, tenant.s, "account", RoleEditor)
			p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a',?,'job','pending',?,?)`, admitted.RunID, []byte(`{"id":"job"}`), []byte(`{}`))
			a := capability.Authorized{Principal: p, Target: capability.Target{Team: "a", RunID: admitted.RunID}}
			if err := tenant.CreateAuthorizedDebugPause(t.Context(), a, "job", "pause argument-secret", time.Time{}, "pause"); err != nil {
				t.Fatal(err)
			}
			pause, err := tenant.GetActiveDebugPause(t.Context(), admitted.RunID, "job")
			if err != nil || pause.Reason != "pause ***" {
				t.Fatalf("pause=%+v %v", pause, err)
			}
		})
	}
}

func TestAccountDeletionDropsOnlyItsPrivateControlReceiptsProperty(t *testing.T) {
	property := func(seed uint64) bool {
		tenant, ref, _, _, _ := approvalReaperFixture(t, "fail")
		first := fmt.Sprintf("account-%016x", seed)
		var survivor capability.Authorized
		for _, id := range []string{first, "survivor"} {
			actor := authorityMember(t, tenant.s, id, RoleEditor)
			p, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			a := capability.Authorized{Principal: p, Target: capability.Target{Team: "a", RunID: string(ref.RunID)}}
			if err := tenant.CreateAuthorizedDebugPause(t.Context(), a, string(ref.NodeID), "manual", time.Time{}, "same-key"); err != nil {
				t.Fatal(err)
			}
			survivor = a
		}
		if _, err := tenant.s.DeleteAccount(t.Context(), first); err != nil {
			t.Fatal(err)
		}
		var deleted, survived int
		if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM run_control_requests WHERE subject_id=?`, first).Scan(&deleted); err != nil {
			t.Fatal(err)
		}
		if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM run_control_requests WHERE subject_id='survivor'`).Scan(&survived); err != nil {
			t.Fatal(err)
		}
		if deleted != 0 || survived != 1 {
			t.Logf("receipts deleted-account=%d survivor=%d", deleted, survived)
			return false
		}
		return tenant.CreateAuthorizedDebugPause(t.Context(), survivor, string(ref.NodeID), "manual", time.Time{}, "same-key") == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}
