package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestReaperExpiresUnclaimedOwnerAndJobQueues(t *testing.T) {
	for _, kind := range []string{"owner", "job"} {
		t.Run(kind, func(t *testing.T) {
			tenant, now, lease := ownerReaperFixture(t, false, 0, runspec.OriginTrigger)
			request := runspec.SealedRequest{AdmittedAt: now.Add(-time.Minute), Request: runspec.Request{Options: runspec.Options{ClaimWait: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Minute}}}}}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=?,claim_wait_ns=?,created_at=?,status='pending' WHERE id='run'`, raw, int64(time.Minute), now.Add(-time.Minute).UnixNano())
			rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE id='run'`)
			if kind == "job" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='claimed',claimed_by='live',lease_expires_at=? WHERE id='run'`, now.Add(time.Hour).UnixNano())
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,ready_at) VALUES ('a','run','queued','pending',?, ?,?)`, []byte(`{"id":"queued"}`), []byte(`{}`), now.Add(-time.Minute).UnixNano())
			}
			*now = now.Add(-time.Nanosecond)
			if reaped, err := tenant.s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
				t.Fatalf("early expiry = %+v, %v", reaped, err)
			}
			*now = now.Add(time.Nanosecond)
			reaped, err := tenant.s.ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Failed || reaped[0].Disruptions != 0 {
				t.Fatalf("queue expiry = %+v, %v", reaped, err)
			}
		})
	}
}

func TestReaperApprovalDeadlineAndCancellation(t *testing.T) {
	for _, policy := range []string{"approve", "deny", "cancel", "indefinite"} {
		t.Run(policy, func(t *testing.T) {
			tenant, now, lease := ownerReaperFixture(t, false, 0, runspec.OriginTrigger)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET lease_expires_at=? WHERE id='run'`, now.Add(time.Hour).UnixNano())
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','approval','approval_pending',?,?)`, []byte(`{"id":"approval","approval":{}}`), []byte(`{}`))
			timeout := time.Minute.Milliseconds()
			storedPolicy := policy
			if policy == "indefinite" {
				timeout, storedPolicy = 0, "deny"
			}
			if policy == "cancel" {
				storedPolicy = "approve"
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO approvals(team,run_id,node_id,requested_at,timeout_ms,on_timeout) VALUES ('a','run','approval',?,?,?)`, now.Add(-time.Minute).UnixNano(), timeout, storedPolicy)
			reaped, err := tenant.s.ReapUnits(t.Context(), lease, 10)
			if err != nil {
				t.Fatal(err)
			}
			node, err := tenant.ReadNode(t.Context(), NodeRef{"run", "approval"})
			if err != nil {
				t.Fatal(err)
			}
			want := NodeFailed
			if policy == "approve" {
				want = NodeSuccess
			}
			if policy == "cancel" {
				want = NodeCancelled
			}
			if policy == "indefinite" {
				if len(reaped) != 0 || node.Status != StatusApprovalPending {
					t.Fatal("indefinite approval expired")
				}
			} else if len(reaped) != 1 || node.Outcome != want {
				t.Fatalf("approval = %+v, outcome = %s", reaped, node.Outcome)
			}
		})
	}
}
