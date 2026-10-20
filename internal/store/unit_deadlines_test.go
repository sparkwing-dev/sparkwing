package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestReaperQueueDeadlineKeepsLiveClaimsAndFreshReleases(t *testing.T) {
	s, demand := unitClaimFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: claim.RunID}
	if _, err := s.ClaimUnit(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(runspec.SealedRequest{Request: runspec.Request{Options: runspec.Options{ClaimWait: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Minute}}}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,claim_wait_ns=?`, request, int64(time.Minute))
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET queue_started_at=? WHERE node_id='job'`, now.Add(-time.Hour).UnixNano())
	for _, id := range []string{"old", "fresh"} {
		started := now
		if id == "old" {
			started = now.Add(-time.Minute)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,ready_at,queue_started_at) VALUES ('a','run',?,'pending',?,?,?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{}`), started.UnixNano(), started.UnixNano())
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := s.ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || reaped[0].NodeID != "old" || !reaped[0].Failed {
		t.Fatalf("queue expiry: %+v, %v", reaped, err)
	}
	tenant := &Tenant{s: s, team: claim.Team}
	old, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "old"})
	if err != nil || old.Outcome != NodeFailed || old.ReadyAt != nil {
		t.Fatalf("expired row: %+v, %v", old, err)
	}
	var reason string
	if err := s.db.QueryRow(`SELECT failure_reason FROM nodes WHERE team='a' AND run_id='run' AND node_id='old'`).Scan(&reason); err != nil || reason != "queue_timeout" {
		t.Fatalf("failure reason: %s, %v", reason, err)
	}
	fresh, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "fresh"})
	if err != nil || fresh.ReadyAt == nil || fresh.Status != StatusPending {
		t.Fatalf("fresh release: %+v, %v", fresh, err)
	}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	if _, err := tenant.RenewNodeClaim(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, actor); err != nil {
		t.Fatalf("live claim expired by queue wait: %v", err)
	}
}

func TestReaperUsesAcceptedPlanClaimWait(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchController)
	request.Seal.Plan = []byte(`{"claim_wait_ms":60000,"jobs":[{"id":"job","steps":[{"id":"step"}]}]}`)
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(request.Seal.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, _ = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: plan})
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := tenant.s.ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Failed {
		t.Fatalf("accepted claim wait: %+v, %v", reaped, err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != RunFailed {
		t.Fatalf("expired plan state: %s, %v", run.State, err)
	}
}

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
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,ready_at,queue_started_at) VALUES ('a','run','queued','pending',?, ?,?,?)`, []byte(`{"id":"queued"}`), []byte(`{}`), now.Add(-time.Minute).UnixNano(), now.Add(-time.Minute).UnixNano())
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
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "approval"})
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

func TestReaperRetryQueueStartsAtNewRunCreation(t *testing.T) {
	tenant, now, lease := ownerReaperFixture(t, false, 0, runspec.OriginTrigger)
	request := runspec.SealedRequest{AdmittedAt: now.Add(-24 * time.Hour), Request: runspec.Request{Options: runspec.Options{ClaimWait: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Minute}}}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=?,created_at=?,claim_wait_ns=?,status='pending',lineage_json=? WHERE id='run'`, raw, now.UnixNano(), int64(time.Minute), []byte(`{"retry_of":"earlier"}`))
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE id='run'`)
	actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, Holder: lease.Holder, Generation: lease.Generation}}
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunFailed, Cause: RunQueueTimeout, Actor: actor}); err == nil {
		t.Fatal("old seal expired the new queue")
	}
	if got, err := tenant.s.ReapUnits(t.Context(), lease, 10); err != nil || len(got) != 0 {
		t.Fatalf("early reaping: %+v, %v", got, err)
	}
	*now = now.Add(time.Minute)
	lease, err = tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tenant.s.ReapUnits(t.Context(), lease, 10)
	if err != nil || len(got) != 1 || !got[0].Failed {
		t.Fatalf("queue deadline: %+v, %v", got, err)
	}
}
