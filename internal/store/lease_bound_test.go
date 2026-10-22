package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestUnmeteredNodeRenewalKeepsCapabilityExpiry(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner", true: "local"}[local], func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			now := s.now()
			s.clock = func() time.Time { return now }
			expiry := now.Add(limits.DefaultLease / 2)
			root := authorityRoot("unmetered", "a")
			root.ExpiresAt = &expiry
			if local {
				root.Subject.Kind = capability.SubjectLocal
			}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix = root.Prefix
			claim, actor := meteredUnitClaim(t, s, demand)
			if !claim.Award.LeaseExpiresAt.Equal(expiry) {
				t.Fatalf("claim expiry = %v, want capability expiry %v", claim.Award.LeaseExpiresAt, expiry)
			}
			tenant := &Tenant{s: s, team: "a"}
			admitExecution(t, tenant, actor)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
			beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || beat.CancelRequested || !beat.ExpiresAt.Equal(expiry) {
				t.Fatalf("unmetered renewal = %+v, %v; want capability expiry %v", beat, err, expiry)
			}
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
			now = expiry
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatal("unmetered lease renewed past capability expiry")
			}
		})
	}
}

func TestUnstartedMeteredOwnerRefusalKeepsDurableDrain(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		for _, refusal := range []string{"balance", "hold"} {
			t.Run(string(kind)+"/"+refusal, func(t *testing.T) {
				s, _, claim, now := launcherSnapshotFixture(t, kind)
				issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				tenant := &Tenant{s: s, team: "a"}
				if refusal == "balance" {
					rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
				} else if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "dispute", "", "dispute opened", *now); err != nil {
					t.Fatal(err)
				}
				beat, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true)
				if err != nil || !beat.CancelRequested {
					t.Fatalf("refused renewal: %+v %v", beat, err)
				}
				var drain int64
				if err := s.db.QueryRowContext(t.Context(), `SELECT drain_deadline_at FROM billing_awards WHERE team='a' AND run_id='run' AND unit_kind=?`, kind).Scan(&drain); err != nil {
					t.Fatal(err)
				}
				deadline := time.Unix(0, drain)
				if !deadline.After(*now) || deadline.After(now.Add(SliceSeconds*time.Second)) || beat.ExpiresAt.After(deadline) {
					t.Fatalf("refusal lease %v escaped drain %v at %v", beat.ExpiresAt, deadline, *now)
				}
				unitBillingTotal(t, s, 0, true)
				for range 4 {
					*now = now.Add(10 * time.Second)
					beat, err = s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true)
					if err != nil || !beat.CancelRequested || beat.ExpiresAt.After(deadline) {
						t.Fatalf("ignored cancellation extended settled award: %+v %v; drain %v", beat, err, deadline)
					}
				}
				*now = deadline
				if _, err = s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err == nil {
					t.Fatal("settled unstarted award renewed past its durable drain")
				}
				rewriteSchemaCount(t, s.db, "billable_intervals", 0)
				unitBillingTotal(t, s, 0, true)
			})
		}
	}
}

func TestCancelledUnstartedMeteredOwnerCannotRenewRefundedAward(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, _ := launcherSnapshotFixture(t, kind)
			issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			operator := authorityMember(t, s, "operator", RoleOwner)
			operator.Kind = ActorOperator
			tenant := &Tenant{s: s, team: "a"}
			if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: operator, Reason: "user_cancelled"}); err != nil {
				t.Fatal(err)
			}
			unitBillingTotal(t, s, 0, true)
			if beat, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err == nil {
				t.Fatalf("a refunded unstarted award regained execution authority: %+v", beat)
			}
			rewriteSchemaCount(t, s.db, "billable_intervals", 0)
		})
	}
}

func TestMeteredSelfClaimBoundsItsOwnerAndPersistsFundingStop(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, false)
	s.claimProfiles = claimProfileFake{profile: match.Profile{Labels: []string{"sparkwing-4cpu"}}}
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	owner, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	self, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone})
	if err != nil || self.Kind != Execute || self.Node.OwnerCapabilityPrefix != actor.CapabilityPrefix {
		t.Fatalf("metered self claim: %+v %v", self, err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: self.Claim.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, holder, err := renewalActor(principal.Capability)
	if err != nil {
		t.Fatal(err)
	}
	admitExecution(t, tenant, holder)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: self.Node.Ref, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "dispute", "", "dispute opened", *now); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true)
	if err != nil || !beat.CancelRequested || beat.ExpiresAt.After(self.Claim.LeaseExpiresAt) {
		t.Fatalf("self claim escaped owner funding stop: %+v %v", beat, err)
	}
	row, err := tenant.ReadNode(t.Context(), self.Node.Ref)
	if err != nil || row.CancelRequestedAt == nil || !row.LeaseExpiresAt.Equal(beat.ExpiresAt) {
		t.Fatalf("self-claimed job escaped owner cancellation: %+v %v", row, err)
	}
	*now = beat.ExpiresAt
	if _, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true); err == nil {
		t.Fatal("self-claim owner renewed past the funding stop")
	}
}

func TestSelfClaimedJobFundingStopPreservesFundedOwner(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, false)
	s.claimProfiles = claimProfileFake{profile: match.Profile{Labels: []string{"sparkwing-4cpu"}}}
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	owner, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	self, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone})
	if err != nil || self.Kind != Execute {
		t.Fatalf("self claim: %+v %v", self, err)
	}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: self.Claim.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, holder, err := renewalActor(principal.Capability)
	if err != nil {
		t.Fatal(err)
	}
	admitExecution(t, tenant, holder)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: self.Node.Ref, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=120+(SELECT SUM(amount_micro) FROM credit_charges WHERE team='a') WHERE team='a'`)
	*now = now.Add(10 * time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, false)
	if err != nil {
		t.Fatal(err)
	}
	var ownerStop, childStop string
	if err := s.db.QueryRow(`SELECT stop_reason FROM billing_awards WHERE team='a' AND unit_kind='trigger'`).Scan(&ownerStop); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT stop_reason FROM billing_awards WHERE team='a' AND unit_kind='node'`).Scan(&childStop); err != nil {
		t.Fatal(err)
	}
	if ownerStop != "" || childStop == "" {
		t.Fatalf("unexpected stops owner=%q child=%q", ownerStop, childStop)
	}
	if beat.CancelRequested {
		t.Fatalf("funded owner stopped for its child's refusal: %+v ownerStop=%q childStop=%q", beat, ownerStop, childStop)
	}
	row, err := tenant.ReadNode(t.Context(), self.Node.Ref)
	if err != nil || row.CancelRequestedAt == nil || row.LeaseExpiresAt == nil || row.LeaseExpiresAt.After(self.Claim.LeaseExpiresAt) {
		t.Fatalf("child stop escaped its lease: %+v, %v", row, err)
	}
	stoppedExpiry := *row.LeaseExpiresAt
	for _, at := range []time.Time{now.Add(10 * time.Second), stoppedExpiry.Add(time.Second)} {
		*now = at
		beat, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, false)
		if err != nil || beat.CancelRequested {
			t.Fatalf("stopped child ended owner renewal: %+v, %v", beat, err)
		}
		row, err := tenant.ReadNode(t.Context(), self.Node.Ref)
		if err != nil || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.Equal(stoppedExpiry) {
			t.Fatalf("owner extended stopped child's lease: %+v, %v", row.LeaseExpiresAt, err)
		}
	}
}
