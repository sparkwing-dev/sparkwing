package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestAuthenticationSnapshotReadsAncestryAndCurrentJob(t *testing.T) {
	tenant, now, _ := liveHolderFixture(t)
	parent := authorityRoot("root", "a")
	authorityPersist(t, tenant.s, parent)
	claim := authorityRoot("claim", "a")
	claim.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix='claim'`)
	authorityPersist(t, tenant.s, claim)
	evidence := capability.RequestEvidence{Target: capability.Target{Team: "b", RunID: "foreign", NodeID: "forged"}}
	got, err := tenant.s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: claim.Digest, Audience: capability.Controller}, evidence)
	if err != nil || len(got.Authorities) != 2 || got.Now != *now {
		t.Fatalf("authentication snapshot: %+v, %v", got, err)
	}
	if got.Authorities[0].Principal.Authority.ID != "claim" || got.Authorities[1].Principal.Authority.ID != "root" || !capability.CredentialAlive(got.Authorities[0].Principal.Capability, got.Now) || !capability.CredentialAlive(got.Authorities[1].Principal.Capability, got.Now) {
		t.Fatalf("ancestry: %+v", got.Authorities)
	}
	if got.Binding.Current.RunID != "run" || got.Binding.Current.NodeID != "job" || got.Binding.Current.Generation != 2 || got.Binding.Current.Attempt != 3 || got.Binding.Current.Holder != "runner" || !got.Binding.LeaseExpiresAt.After(*now) {
		t.Fatalf("binding: %+v", got.Binding)
	}
}

func TestAuthenticationSnapshotRetainsFinalAttemptFenceAndLogState(t *testing.T) {
	tenant, _, actor := liveHolderFixture(t)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(3)}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,prefix,retention_class,sealed_at,created_at,updated_at) VALUES ('a','run','job-stream','job_attempt','job',3,2,'logs','r30',1,1,1)`)
	got, err := tenant.s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256([]byte("claim")), Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil || got.Binding.Current.Generation != 2 || got.Binding.Current.Attempt != 3 || got.Binding.Current.Holder != "runner" || got.Binding.FinishedAt == nil || !got.Binding.LogSealed || !got.Binding.LeaseExpiresAt.IsZero() {
		t.Fatalf("final binding: %+v, %v", got.Binding, err)
	}
}

func TestAuthenticationSnapshotRechecksAncestryAndMembership(t *testing.T) {
	for _, problem := range []string{"digest", "audience", "revoked", "expired", "membership", "session", "class"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Unix(1700000000, 0).UTC()
			s.clock = func() time.Time { return now }
			member := authorityMember(t, s, "member", RoleEditor)
			child := authorityRoot("child", "a")
			child.Parent = &member.Authority
			authorityPersist(t, s, child)
			lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: child.Digest, Audience: capability.Controller}
			switch problem {
			case "digest":
				lookup.Digest[0] ^= 1
			case "audience":
				lookup.Audience = capability.NodeHost
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, member.Authority.ID)
			case "expired":
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=? WHERE hash=?`, now.UnixNano(), member.Authority.ID)
			case "membership":
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='member'`)
			case "session":
				rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash=?`, member.Authority.ID)
			case "class":
				lookup.Class = capability.CredentialSession
			}
			_, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.Unauthenticated {
				t.Fatalf("unavailable %s: %v", problem, err)
			}
		})
	}
}

func TestAuthenticationSnapshotLooksUpSessionDigestWithoutBearer(t *testing.T) {
	s := authorityStore(t)
	digest := sha256.Sum256([]byte("session-presentation"))
	hash := hex.EncodeToString(digest[:])
	authorityMember(t, s, hash, RoleEditor)
	got, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: digest, Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil || len(got.Authorities) != 1 || got.Authorities[0].Principal.Authority.ID != hash || got.Authorities[0].Principal.Class != capability.CredentialSession || !capability.CredentialAlive(got.Authorities[0].Principal.Capability, got.Now) {
		t.Fatalf("session: %+v, %v", got, err)
	}
}

func TestAuthenticationSnapshotSeparatesEvalAndSourceLiveness(t *testing.T) {
	tenant, now, _ := expansionFixture(t)
	unitID, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	eval := authorityRoot("eval", "a")
	eval.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: unitID, Holder: "eval-host", Generation: 8}}
	authorityPersist(t, tenant.s, eval)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, now.Add(time.Minute).UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation) VALUES ('a','run','job',2,'running',8,'eval-host','eval',?,4)`, now.Add(time.Minute).UnixNano())
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: eval.Digest, Audience: capability.Controller}
	for _, owner := range []string{"live", "replaced", "absent"} {
		if owner == "replaced" {
			rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=5 WHERE name='controller.owner'`)
		}
		if owner == "absent" {
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM leases WHERE name='controller.owner'`)
		}
		got, err := tenant.s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
		if err != nil || got.Binding.Current.Generation != 2 || got.Binding.Expansion == nil || got.Binding.Expansion.Lease.Generation != 8 || got.Binding.Expansion.Lease.Holder != "eval-host" || !got.Binding.Expansion.ExpiresAt.After(*now) {
			t.Fatalf("eval snapshot %s: %+v, %v", owner, got.Binding, err)
		}
		if (got.Binding.Expansion.ControllerLeaseExpiresAt != nil) != (owner == "live") {
			t.Fatalf("controller fence %s: %+v", owner, got.Binding.Expansion)
		}
	}
}

func TestAuthenticationSnapshotHereClaimHistorySurvivesRequeue(t *testing.T) {
	tenant, _, actor := reconcileFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE id='run'`)
	got, err := tenant.s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256([]byte(actor.CapabilityPrefix)), Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil || !got.Binding.HereClaimed || got.Binding.Current.Generation != 1 || got.Binding.Current.Holder != "" || !got.Binding.LeaseExpiresAt.IsZero() {
		t.Fatalf("requeued owner: %+v, %v", got.Binding, err)
	}
}

func TestAuthenticationSnapshotReadsPlanLeaseAndCancellation(t *testing.T) {
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	launcher := authorityRoot("launcher", "")
	launcher.Subject.Kind = capability.SubjectService
	launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, launcher)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	demand.CapabilityPrefix = launcher.Prefix
	demand.Target = &UnitRef{Kind: capability.UnitPlan, RunID: "run"}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
	got, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256(claim.Bearer), Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil || !got.Binding.RunCancelled || got.Binding.Current.Kind != capability.BindingRun || got.Binding.Current.Holder != "launcher" || got.Binding.Current.Generation != int64(claim.Award.Generation) || got.Binding.LeaseExpiresAt != claim.Award.LeaseExpiresAt {
		t.Fatalf("plan liveness: %+v, %v", got.Binding, err)
	}
}

func TestAuthenticationCancellationReasonKeepsJobIntentProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(483, 771))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, _, owner := reconcileFixture(t)
			operator := authorityMember(t, tenant.s, "operator", RoleOwner)
			operator.Kind = ActorOperator
			reason := fmt.Sprintf("%s/%x", []string{"user_cancelled", "雪\n\x00", "' OR 1=1 --"}[random.IntN(3)], random.Uint64())
			if i == 0 {
				reason = ""
			}
			_, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: owner, To: NodeCancelled, Cause: CauseCancel, Extra: transitionExtra{Result: &NodeResult{Outcome: NodeCancelled, Reason: reason}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", Actor: operator, To: RunCancelled, Cause: RunCancel, Reason: "run reason"}); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"claim", owner.CapabilityPrefix} {
				got, err := tenant.s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: sha256.Sum256([]byte(prefix)), Audience: capability.Controller}, capability.RequestEvidence{Target: capability.Target{RunID: "forged"}})
				want := "run reason"
				if prefix == "claim" {
					want = reason
				}
				if err != nil || !got.Binding.RunCancelled || got.Binding.CancelReason != want {
					t.Fatalf("%s cancellation: %+v, %v; want %q", prefix, got.Binding, err, want)
				}
			}
		})
	}
}
