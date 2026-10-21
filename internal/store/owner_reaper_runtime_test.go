package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func ownerReaperFixture(t *testing.T, started bool, disruptions int, origin runspec.Origin) (*Tenant, *time.Time, SingletonLease) {
	t.Helper()
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	request, err := json.Marshal(runspec.SealedRequest{Version: 1, AdmittedAt: now.Add(-time.Hour), Request: runspec.Request{Origin: origin}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, request)
	var start any
	if started {
		start = now.Add(-time.Minute).UnixNano()
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,started_at,disruptions,needs_labels,prefers_labels,claimed_at) VALUES ('a','run','claimed','trigger',1,1,'owner','owner-cap',7,?,?,?,?,?,1)`, now.UnixNano(), start, disruptions, []byte(`[]`), []byte(`[]`))
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, now, lease
}

func TestOwnerReaperRecoversSameRun(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstarted", true: "started"}[started], func(t *testing.T) {
			tenant, _, lease := ownerReaperFixture(t, started, 1, runspec.OriginTrigger)
			reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].Generation != 7 {
				t.Fatalf("reap = %+v, %v", reaped, err)
			}
			var status string
			var generation, created, disruptions int64
			var holder sql.NullString
			var expiry, start sql.NullInt64
			err = tenant.s.db.QueryRow(`SELECT status,generation,created_at,disruptions,claimed_by,lease_expires_at,started_at FROM triggers WHERE id='run'`).Scan(&status, &generation, &created, &disruptions, &holder, &expiry, &start)
			wantDisruptions := int64(1)
			if started {
				wantDisruptions++
			}
			if err != nil || status != "pending" || generation != 7 || created != 1 || disruptions != wantDisruptions || holder.Valid || expiry.Valid || start.Valid {
				t.Fatalf("owner = %s %d %d %d %v %v %v, %v", status, generation, created, disruptions, holder, expiry, start, err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != RunRunning || run.CancelRequestedAt != nil {
				t.Fatalf("run = %+v, %v", run, err)
			}
			repeated, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
			if err != nil || len(repeated) != 0 {
				t.Fatalf("repeat reap = %+v, %v", repeated, err)
			}
		})
	}
}

func TestOwnerReaperTerminalPolicies(t *testing.T) {
	for _, mode := range []string{"cap", "local", "cancel", "here_deadline", "plan_cap"} {
		t.Run(mode, func(t *testing.T) {
			tenant, now, lease := ownerReaperFixture(t, true, 2, runspec.OriginTrigger)
			switch mode {
			case "local":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`{"request":{"origin":"local"}}`))
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET disruptions=0`)
			case "cancel":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			case "here_deadline":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET disruptions=0,expires_at=?`, now.UnixNano())
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`{"request":{"options":{"runner":{"set":true,"value":{"kind":"here"}}}}}`))
			case "plan_cap":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller' WHERE id='run'`)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET unit_kind='plan'`)
			}
			reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || reaped[0].Requeued {
				t.Fatalf("reap = %+v, %v", reaped, err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			want := RunFailed
			if mode == "cancel" {
				want = RunCancelled
			}
			if err != nil || run.State != want || run.FinishedAt == nil {
				t.Fatalf("run = %+v, %v", run, err)
			}
			var claims int
			if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM triggers WHERE claimed_by IS NOT NULL OR lease_expires_at IS NOT NULL`).Scan(&claims); err != nil || claims != 0 {
				t.Fatalf("remaining claims = %d, %v", claims, err)
			}
		})
	}
}

func TestOwnerReaperPreservesRemoteClaimAndDrainsBeforeFailure(t *testing.T) {
	for _, disruptions := range []int{0, 2} {
		t.Run(map[int]string{0: "resume", 2: "cap"}[disruptions], func(t *testing.T) {
			tenant, now, lease := ownerReaperFixture(t, true, disruptions, runspec.OriginTrigger)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,generation,attempt,claimed_by,capability_prefix,lease_expires_at,started_at) VALUES ('a','run','remote','running',?, ?,4,1,'remote-runner','remote-cap',?,?)`, []byte(`{"id":"remote"}`), []byte(`{}`), now.Add(time.Minute).UnixNano(), now.Add(-time.Minute).UnixNano())
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','remote',1,4,'remote-runner','remote-cap','runner','remote','remote',?)`, now.Add(-time.Minute).UnixNano())
			if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
				t.Fatal(err)
			}
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "remote"})
			if err != nil || node.Status != StatusRunning || node.ClaimedBy != "remote-runner" || (node.CancelRequestedAt != nil) != (disruptions == 2) {
				t.Fatalf("remote claim = %+v, %v", node, err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != RunRunning {
				t.Fatalf("undrained run = %+v, %v", run, err)
			}
			if disruptions == 2 {
				*now = now.Add(time.Minute)
				if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
					t.Fatal(err)
				}
				run, err = tenant.ReadRun(t.Context(), "run")
				if err != nil || run.State != RunFailed {
					t.Fatalf("drained run = %+v, %v", run, err)
				}
			}
		})
	}
}

func TestOwnerReaperEvalRecoveryAndBarrier(t *testing.T) {
	for _, mode := range []string{"unstarted", "started", "cap", "cancel", "controller_lapse", "already_accepted"} {
		t.Run(mode, func(t *testing.T) {
			tenant, now, request := expansionFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM triggers WHERE id='run'`)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
			until := now.Add(time.Hour).UnixNano()
			if mode == "controller_lapse" {
				until = now.UnixNano()
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, until)
			status, disruptions := "claimed", 0
			var started any
			if mode != "unstarted" {
				status, started = "running", now.Add(-time.Minute).UnixNano()
			}
			if mode == "cap" {
				disruptions = 2
			}
			if mode == "cancel" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			}
			if mode == "already_accepted" {
				body := json.RawMessage(`{"jobs":[]}`)
				digest, err := runspec.CanonicalHash(struct {
					Body         json.RawMessage
					Registration runspec.SecretRegistration
				}{body, runspec.SecretRegistration{}})
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET expansion='accepted',expansion_body=?,expansion_registration=?,expansion_digest=? WHERE node_id='job'`, body, []byte(`{}`), digest)
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation,started_at,disruptions,claimed_at) VALUES ('a','run','job',2,?,8,'evaluator','eval-cap',?,4,?,?,1)`, status, now.UnixNano(), started, disruptions)
			lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || reaped[0].Kind != "eval" {
				t.Fatalf("reap = %+v, %v", reaped, err)
			}
			if err := tenant.s.db.QueryRow(`SELECT status,disruptions FROM eval_units`).Scan(&status, &disruptions); err != nil {
				t.Fatal(err)
			}
			node, err := tenant.ReadNode(t.Context(), request.Source)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantExpansion := "pending", ExpansionExpected
			if mode == "cap" {
				wantStatus, wantExpansion = "failed", ExpansionFailed
			}
			if mode == "cancel" {
				wantStatus, wantExpansion = "cancelled", ExpansionNone
			}
			if mode == "already_accepted" {
				wantStatus, wantExpansion = "done", ExpansionAccepted
			}
			if status != wantStatus || node.Expansion != wantExpansion {
				t.Fatalf("eval = %s, expansion = %s", status, node.Expansion)
			}
			wantDisruptions := 1
			if mode == "unstarted" || mode == "cancel" || mode == "already_accepted" {
				wantDisruptions = 0
			}
			if mode == "cap" {
				wantDisruptions = 3
			}
			if disruptions != wantDisruptions {
				t.Fatalf("disruptions = %d, want %d", disruptions, wantDisruptions)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cap" && run.State != RunFailed || mode == "cancel" && run.State != RunCancelled || mode == "controller_lapse" && run.State != RunRunning {
				t.Fatalf("run state = %s", run.State)
			}
		})
	}
}

func TestOwnerReaperLeavesControllerOwnershipToElection(t *testing.T) {
	tenant, now, _ := expansionFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='done',unit_kind='plan',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL,finished_at=1 WHERE id='run'`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','previous',4,?)`, now.UnixNano())
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 0 {
		t.Fatalf("controller-owned reap = %+v, %v", reaped, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != RunRunning {
		t.Fatalf("controller run = %s, %v", run.State, err)
	}
	*now = now.Add(time.Minute)
	if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err == nil {
		t.Fatal("expired reaper accepted an empty sweep")
	}
}

func TestOwnerReaperRechecksControllerHandoff(t *testing.T) {
	for _, mode := range []string{"live", "lapsed", "missing"} {
		t.Run(mode, func(t *testing.T) {
			tenant, request, now := planFixture(t, runspec.DispatchController)
			s := tenant.s
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET lease_expires_at=? WHERE id=?`, now.UnixNano(), request.RunID)
			stale, err := s.AsOperator().expiredUnits(t.Context(), 10)
			if err != nil || len(stale) != 1 {
				t.Fatalf("expired planner: %+v, %v", stale, err)
			}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if reaped, err := s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("recover planner: %+v, %v", reaped, err)
			}
			launcher := authorityRoot("launcher", "")
			launcher.Subject.Kind = capability.SubjectService
			launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
			authorityPersist(t, s, launcher)
			fundUnitClaim(t, s)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: launcher.Prefix, Target: &UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(request.RunID)}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
			if err != nil {
				t.Fatal(err)
			}
			issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			request.Actor = Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Owner: claim.Owner}
			if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "lapsed":
				rewriteSchemaExec(t, s.db, `UPDATE leases SET until=? WHERE name='controller.owner'`, now.UnixNano())
			case "missing":
				rewriteSchemaExec(t, s.db, `DELETE FROM leases WHERE name='controller.owner'`)
			}
			before, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
			// safety: another pass can commit recovery and plan acceptance
			// between candidate selection and the per-unit transaction.
			err = s.inTx(t.Context(), func(tx *storeTx) error {
				if err := assertSingletonLeaseTx(t.Context(), tx, lease, "controller.reaper"); err != nil {
					return err
				}
				reaped, err := reapOwnerUnitTx(t.Context(), tx, stale[0], actor, transactionTime(t, tx))
				if err == nil && reaped != nil {
					t.Errorf("stale candidate recovered accepted plan: %+v", reaped)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM triggers WHERE id=?`, request.RunID).Scan(&status); err != nil || status != "done" {
				t.Fatalf("accepted planner: %q, %v", status, err)
			}
			after, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("stale candidate changed controller run: %+v, %v", after, err)
			}
		})
	}
}

func TestRunOwnerLostRefusesUnstartedLocalOwner(t *testing.T) {
	tenant, _, lease := ownerReaperFixture(t, false, 0, runspec.OriginLocal)
	actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunFailed, Cause: RunOwnerLost, Actor: actor}); err == nil {
		t.Fatal("unstarted local owner failed as an orphan")
	}
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("unstarted local recovery = %+v, %v", reaped, err)
	}
}

func TestOwnerRequeueEmitsOneCommittedEvent(t *testing.T) {
	tenant, _, lease := ownerReaperFixture(t, true, 0, runspec.OriginTrigger)
	for range 2 {
		if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
			t.Fatal(err)
		}
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 20)
	if err != nil || len(events) != 1 || events[0].Kind != "run_requeued" || events[0].NodeID != "" {
		t.Fatalf("owner recovery events: %+v, %v", events, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != RunRunning || run.StateSeq != events[0].StateSeq {
		t.Fatalf("owner recovery state: %+v, %v", run, err)
	}
}

func TestOwnerRequeueRollsBackWhenEventCannotCommit(t *testing.T) {
	tenant, _, lease := ownerReaperFixture(t, true, 0, runspec.OriginTrigger)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_requeue BEFORE INSERT ON events WHEN NEW.kind='run_requeued' BEGIN SELECT RAISE(ABORT,'event unavailable'); END`)
	if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err == nil {
		t.Fatal("owner recovery ignored failed event write")
	}
	var status, holder string
	var disruptions int
	if err := tenant.s.db.QueryRow(`SELECT status,claimed_by,disruptions FROM triggers WHERE id='run'`).Scan(&status, &holder, &disruptions); err != nil || status != "claimed" || holder != "owner" || disruptions != 0 {
		t.Fatalf("failed event changed owner: %s, %s, %d, %v", status, holder, disruptions, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.StateSeq != 0 {
		t.Fatalf("failed event changed run sequence: %d, %v", run.StateSeq, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "events", 0)
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_requeue`)
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("recovery retry: %+v, %v", reaped, err)
	}
}

func TestOwnerReaperRecoversEveryTeamWithoutFailingRemoteRuns(t *testing.T) {
	s, _, now := maintenanceStore(t)
	for i, team := range []Team{"a", "b"} {
		id := "run-" + string(team)
		maintenanceRun(t, s, id, i+1, RunRunning)
		request := []byte(`{"request":{"origin":"local"}}`)
		if team == "b" {
			request = []byte(`{"request":{"origin":"trigger"}}`)
		}
		rewriteSchemaExec(t, s.db, `UPDATE runs SET team=?,request_json=? WHERE id=?`, team, request, id)
		rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,started_at,needs_labels,prefers_labels,claimed_at) VALUES (?,?,'claimed','trigger',1,1,'owner','owner-cap',2,?,?,?,?,1)`, team, id, now.UnixNano(), now.Add(-time.Minute).UnixNano(), []byte(`[]`), []byte(`[]`))
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 2 {
		t.Fatalf("all-team recovery: %+v, %v", reaped, err)
	}
	for _, team := range []Team{"a", "b"} {
		run, err := (&Tenant{s: s, team: team}).ReadRun(t.Context(), "run-"+string(team))
		want := RunFailed
		if team == "b" {
			want = RunRunning
		}
		if err != nil || run.State != want {
			t.Fatalf("team %s: %+v, %v", team, run, err)
		}
	}
}
