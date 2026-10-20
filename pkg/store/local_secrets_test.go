package store

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func localSecretFixture(t *testing.T, value string) (*Tenant, capability.Authorized) {
	t.Helper()
	tenant, plan, _ := planFixtureWithInput(t, runspec.DispatchHost, func(in *IntakeRequest) {
		in.Request.LocalSecretNames = []string{"KEY"}
		in.LocalSecrets = map[string]string{"KEY": value}
	})
	plan.Seal.Resolved.SecretNames = []string{"KEY"}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	p, err := tenant.s.ResolveAuthority(t.Context(), plan.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	p.Capability.Grants = append(p.Capability.Grants, capability.SecretsRelease)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix=?`, p.Capability.Prefix)
	authorityPersist(t, tenant.s, p.Capability)
	p, err = tenant.s.ResolveAuthority(t.Context(), plan.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "a", SecretName: "KEY"}, Access: capability.AccessRule{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.SecretsRelease}, Binding: capability.BindDeclaredSecret, Liveness: capability.LiveSensitive}}
}

func TestLocalSecretsIntakePlanAndLiveClaim(t *testing.T) {
	tenant, a := localSecretFixture(t, "requester-private-value")
	row, err := tenant.ReadSecret(t.Context(), a)
	if err != nil || row == nil || row.Value != "requester-private-value" || !row.Masked || !row.Ephemeral {
		t.Fatalf("local release: %#v %v", row, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), a.Binding.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == "secret_released" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing release audit")
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET generation=generation+1 WHERE id=?`, a.Binding.RunID)
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatalf("stale claim released: %#v %v", row, err)
	}
}

func TestLocalSecretsRejectMissingAndUndeclaredIntake(t *testing.T) {
	for _, values := range []map[string]string{nil, {}, {"OTHER": "value"}, {"KEY": "value", "OTHER": "value"}} {
		tenant, in, _ := intakeFixture(t)
		in.Request.LocalSecretNames = []string{"KEY"}
		in.LocalSecrets = values
		if _, err := tenant.AdmitRun(t.Context(), in); err == nil {
			t.Fatalf("admitted unmatched values: %#v", values)
		}
		rewriteSchemaCount(t, tenant.s.db, "runs", 0)
	}
}

func TestLocalSecretsReplayRejectsChangedValues(t *testing.T) {
	tenant, in, _ := intakeFixture(t)
	in.IdempotencyKey = "one"
	in.Request.LocalSecretNames = []string{"KEY"}
	in.LocalSecrets = map[string]string{"KEY": "original"}
	first, err := tenant.AdmitRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := tenant.AdmitRun(t.Context(), in); err != nil || !replay.Replayed || replay.RunID != first.RunID {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	initial := in.LocalSecrets["KEY"]
	in.LocalSecrets["KEY"] = "changed"
	if _, err := tenant.AdmitRun(t.Context(), in); err == nil {
		t.Fatal("changed secret reseeded replay")
	}
	tenant.s.localSecrets.Clear()
	in.LocalSecrets["KEY"] = initial
	if _, err := tenant.AdmitRun(t.Context(), in); err == nil {
		t.Fatal("replayed a run after its volatile secrets were lost")
	}
}

func TestLocalSecretsLossFailsInsteadOfFallingBack(t *testing.T) {
	tenant, a := localSecretFixture(t, "requester-private-value")
	if err := seedSecret(t, tenant, Secret{Name: "KEY", Pipeline: "p", Value: "different", Masked: true}, tenant.s.now()); err != nil {
		t.Fatal(err)
	}
	tenant.s.localSecrets.Clear()
	row, err := tenant.ReadSecret(t.Context(), a)
	if err == nil || row != nil {
		t.Fatalf("lost local secret fell back: %#v %v", row, err)
	}
}

func FuzzBurstSecretReadRechecksClaimantEligibility(f *testing.F) {
	f.Add("here")
	f.Add("machines")
	f.Add("cloud")
	f.Fuzz(func(t *testing.T, target string) {
		if target != "here" && target != "machines" && target != "cloud" {
			t.Skip()
		}
		tenant, a := localSecretFixture(t, "requester-private-value")
		run, err := tenant.ReadRun(t.Context(), a.Binding.RunID)
		if err != nil {
			t.Fatal(err)
		}
		run.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: target}}}
		body, err := json.Marshal(run.Request)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, tenant.s.db, "UPDATE runs SET request_json=? WHERE team=? AND id=?", body, tenant.team, run.ID)
		row, err := tenant.ReadSecret(t.Context(), a)
		if err == nil || row != nil {
			t.Fatal("team runner released a requester's burst secret")
		}
	})
}

func FuzzLocalSecretsNeverPersistOrEscapeTheirRun(f *testing.F) {
	f.Add([]byte("seed"), uint8(0))
	f.Add([]byte("other"), uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, attack uint8) {
		if len(data) > 1024 {
			return
		}
		value := "requester-private-" + hex.EncodeToString(data)
		tenant, a := localSecretFixture(t, value)
		run, err := tenant.ReadRun(t.Context(), a.Binding.RunID)
		if err != nil {
			t.Fatal(err)
		}
		events, err := tenant.ReadStateEvents(t.Context(), a.Binding.RunID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal([]any{run, events})
		if err != nil || strings.Contains(string(raw), value) {
			t.Fatal("secret reached persisted projection")
		}
		rewriteSchemaCount(t, tenant.s.db, "secrets", 0)
		// A run-bound read ignores a caller-named pipeline, so it is no escape route to test here.
		switch attack % 4 {
		case 0:
			a.Target.Team = "foreign"
		case 1:
			a.Target.RunID = "foreign"
		case 2:
			a.Target.SecretName = "UNDECLARED"
		case 3:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id=?`, a.Binding.RunID)
		}
		row, err := tenant.ReadSecret(t.Context(), a)
		if err == nil || row != nil || strings.Contains(err.Error(), value) {
			t.Fatal("secret escaped its live declared run")
		}
	})
}

func TestLocalSecretsReleasedOnlyAfterCommittedRunCompletion(t *testing.T) {
	tenant, a := localSecretFixture(t, "requester-private-value")
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1 WHERE team='a' AND run_id=?`, a.Binding.RunID)
	actor := Actor{Kind: ActorRunHost, Authority: a.Principal.Authority, CapabilityPrefix: a.Principal.Capability.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: a.Binding.RunID, Holder: a.Binding.Holder, Generation: a.Binding.Generation}}
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: a.Binding.RunID, To: RunRunning, Cause: RunStart, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	finish := FinishRunRequest{Unit: actor.Owner, Transition: RunTransition{RunID: a.Binding.RunID, To: RunSuccess, Cause: RunFinish, Actor: actor}}
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_local_finish BEFORE UPDATE ON triggers BEGIN SELECT RAISE(ABORT,'failed'); END`)
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
		t.Fatal("finish ignored failed owner write")
	}
	if value, err := tenant.s.localSecret(tenant.team, a.Binding.RunID, "KEY"); err != nil || value != "requester-private-value" {
		t.Fatal("rollback lost requester secret")
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_local_finish`)
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	if _, ok := tenant.s.localSecrets.Load(localSecretKey{tenant.team, a.Binding.RunID}); ok {
		t.Fatal("completed run retained local secret")
	}
}

func TestLocalSecretsPurgedOnTeamDeletionAndOwnerLeaseExpiry(t *testing.T) {
	for _, boundary := range []string{"team deletion", "expired owner"} {
		t.Run(boundary, func(t *testing.T) {
			tenant, a := localSecretFixture(t, "requester-private-value")
			if boundary == "team deletion" {
				if _, _, err := tenant.s.AsOperator().RequestTeamDeletion(t.Context(), tenant.team, tenant.s.now()); err != nil {
					t.Fatal(err)
				}
			} else {
				now := tenant.s.now().Add(time.Minute)
				rewriteSchemaExec(t, tenant.s.db, "UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?", now.UnixNano(), tenant.team, a.Binding.RunID)
				tenant.s.clock = func() time.Time { return now }
				lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if reaped, err := tenant.s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
					t.Fatalf("expired owner did not requeue: %+v %v", reaped, err)
				}
			}
			if _, err := tenant.s.localSecret(tenant.team, a.Binding.RunID, "KEY"); err == nil {
				t.Fatal("inactive requester secret retained")
			}
		})
	}
}

func TestLocalSecretsRetryRequiresFreshRequesterResolution(t *testing.T) {
	tenant, a := localSecretFixture(t, "requester-private-value")
	request := IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, Request: runspec.Request{Team: "a", Pipeline: "p", Lineage: runspec.Lineage{RetryOf: a.Binding.RunID}}}
	_, err := tenant.AdmitRun(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "submit a new run") {
		t.Fatalf("retry failed without requester action: %v", err)
	}
	request.Request.LocalSecretNames = []string{"KEY"}
	request.LocalSecrets = map[string]string{"KEY": "replacement"}
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("retry replaced the original secret snapshot")
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
}

func FuzzCancelledHereResupplyNeverRestoresSecrets(f *testing.F) {
	f.Add([]byte("requester-value"), uint8(0))
	f.Add([]byte("requester-value"), uint8(1))
	f.Add([]byte("requester-value"), uint8(2))
	f.Fuzz(func(t *testing.T, value []byte, attack uint8) {
		if len(value) > 256 {
			t.Skip()
		}
		s, _, owner := burstHereOwnerFixture(t)
		tenant, err := s.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		run, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		run.Request.Request.LocalSecretNames = []string{"KEY"}
		raw, err := json.Marshal(run.Request)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,cancel_requested_at=? WHERE team='a' AND id='run'`, raw, s.now().UnixNano())
		switch attack % 3 {
		case 1:
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=generation+1 WHERE team='a' AND id='run'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET lease_expires_at=? WHERE team='a' AND id='run'`, s.now().UnixNano())
		}
		err = s.ResupplyHereSecrets(t.Context(), owner.Authority, map[string]string{"KEY": string(value)})
		if attack%3 == 0 && err != nil {
			t.Fatalf("cancelled live owner cannot drain: %v", err)
		}
		if attack%3 != 0 && err == nil {
			t.Fatal("lost owner retained resupply authority")
		}
		if _, ok := s.localSecrets.Load(localSecretKey{"a", "run"}); ok {
			t.Fatal("cancelled owner restored secret values")
		}
	})
}
