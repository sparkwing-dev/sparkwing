package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzSubmittedEnvironmentStaysPrivateAndClaimBound(f *testing.F) {
	f.Add([]byte("private submitted value"))
	f.Add([]byte{255, 254, '\n', 'x'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256 {
			return
		}
		tenant, request, now := intakeFixture(t)
		cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
		if err != nil {
			t.Fatal(err)
		}
		tenant.s.runEnvCipher = cipher
		value := "submitted-value-" + string(raw)
		if strings.ContainsRune(value, 0) {
			return
		}
		request.Request.EnvironmentSource = runspec.EnvironmentSubmitted
		request.SubmittedEnv = map[string][]byte{"APP_NOTE": []byte(value), "APP_EMPTY": {}}
		request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"APP_NOTE": value, "APP_EMPTY": ""}, strings.Repeat("5", 64))
		if err != nil {
			t.Fatal(err)
		}
		request.Request.Options.SubmitEnvAllow.Value = []string{"APP_*"}
		request.IdempotencyKey = "same"
		admitted, err := tenant.AdmitRun(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := tenant.s.queryRow(t.Context(), `SELECT submitted_env FROM runs WHERE team=? AND id=?`, tenant.team, admitted.RunID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored == "" || bytes.Contains([]byte(stored), []byte(value)) {
			t.Fatal("submitted environment persisted as plaintext")
		}
		public, err := json.Marshal(admitted)
		if err != nil || bytes.Contains(public, []byte(value)) {
			t.Fatal("private value entered intake receipt")
		}
		if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		request.SubmittedEnv["APP_NOTE"] = []byte("changed-value")
		if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
			t.Fatal("replay replaced private values")
		}
		owner := authorityRoot("env-owner", "a")
		owner.Unit, owner.UnitID, owner.Purpose = capability.UnitTrigger, admitted.RunID, capability.PurposeTrigger
		owner.Parent, owner.Issuance, owner.Delivery = &request.Authority, capability.Award, capability.DeliveryDirect
		owner.Data = &capability.DataScope{RepositoryKey: string(request.Request.Git.Repository.Key)}
		owner.Grants = []capability.Grant{capability.TriggerHost}
		owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
		persistPlanOwner(t, tenant.s, owner)
		seedTriggerClaim(t, tenant.s, admitted.RunID, "host", owner.Prefix, 1, now.Add(time.Minute))
		authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}
		got, err := tenant.ReadSubmittedEnvironment(t.Context(), authority)
		if err != nil || string(got["APP_NOTE"]) != value || len(got) != 2 {
			t.Fatal("live host lost submitted environment", err)
		}
		if _, err := (&Tenant{s: tenant.s, team: "b"}).ReadSubmittedEnvironment(t.Context(), authority); err == nil {
			t.Fatal("foreign tenant read environment")
		}
		if _, err := tenant.ReadSubmittedEnvironment(t.Context(), request.Authority); err == nil {
			t.Fatal("unbound member read environment")
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET generation=2 WHERE id=?`, admitted.RunID)
		if _, err := tenant.ReadSubmittedEnvironment(t.Context(), authority); err == nil {
			t.Fatal("stale host read environment")
		}
	})
}

func TestSubmittedEnvironmentRetryCopiesEncryptedValues(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	tenant.s.runEnvCipher = cipher
	request.Request.EnvironmentSource = runspec.EnvironmentSubmitted
	request.SubmittedEnv = map[string][]byte{"BUILD_NOTE": []byte("retained-private-value")}
	request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"BUILD_NOTE": "retained-private-value"}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	request.Request.Options.SubmitEnvAllow.Value = []string{"BUILD_*"}
	original, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: request.Authority, Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: original.RunID}}})
	if err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := tenant.s.queryRow(t.Context(), `SELECT submitted_env FROM runs WHERE id=?`, original.RunID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := tenant.s.queryRow(t.Context(), `SELECT submitted_env FROM runs WHERE id=?`, retry.RunID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("retry ciphertext was not rebound to its execution")
	}
	read := func() (values map[string][]byte, err error) {
		err = tenant.s.inTx(t.Context(), func(tx *storeTx) error {
			values, err = tenant.readSubmittedEnvironmentTx(t.Context(), tx, retry.RunID)
			return err
		})
		return values, err
	}
	if values, err := read(); err != nil || string(values["BUILD_NOTE"]) != "retained-private-value" {
		t.Fatal("retry lost captured values", err)
	}
	for _, broken := range []string{first, second + "corrupt", ""} {
		rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET submitted_env=? WHERE id=?`, broken, retry.RunID)
		if values, err := read(); err == nil || values != nil {
			t.Fatal("transposed, corrupt or missing environment was released")
		}
	}
}

func TestSubmittedEnvironmentRefusesInvalidIntakeAtomically(t *testing.T) {
	for _, problem := range []string{"missing", "changed", "extra", "credential", "reserved", "unlisted", "no_cipher"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			tenant.s.runEnvCipher = cipher
			name, value := "BUILD_NOTE", "private-value"
			if problem == "credential" {
				name = "BUILD_TOKEN"
			}
			if problem == "reserved" {
				name = "SPARKWING_RUN_ID"
			}
			request.Request.EnvironmentSource = runspec.EnvironmentSubmitted
			request.SubmittedEnv = map[string][]byte{name: []byte(value)}
			request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{name: value}, strings.Repeat("5", 64))
			if err != nil {
				t.Fatal(err)
			}
			request.Request.Options.SubmitEnvAllow.Value = []string{name}
			switch problem {
			case "missing":
				request.Request.EnvironmentSource = runspec.EnvironmentSubmitted
				request.SubmittedEnv = nil
			case "changed":
				request.SubmittedEnv[name] = []byte("different")
			case "extra":
				request.SubmittedEnv["EXTRA"] = []byte("extra-value")
			case "unlisted":
				request.Request.Options.SubmitEnvAllow.Value = nil
			case "no_cipher":
				tenant.s.runEnvCipher = nil
			}
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil || strings.Contains(err.Error(), value) {
				t.Fatal("invalid private environment admitted or disclosed")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
		})
	}
}

func FuzzExecutionEnvironmentUsesOnlyLiveUnitFence(f *testing.F) {
	if testing.Short() {
		f.Skip("slow: 2.0s measured in the short class; the full suite runs it")
	}
	for kind := uint8(0); kind < 3; kind++ {
		for change := uint8(0); change < 11; change++ {
			f.Add(kind, change, "captured-value")
		}
	}
	f.Fuzz(func(t *testing.T, index, change uint8, value string) {
		if len(value) > 256 || strings.ContainsRune(value, 0) {
			return
		}
		kind := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[index%3]
		s, _, claim, now := launcherSnapshotFixture(t, kind)
		tenant := &Tenant{s: s, team: claim.Team}
		cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
		if err != nil {
			t.Fatal(err)
		}
		s.runEnvCipher = cipher
		run, err := tenant.ReadRun(t.Context(), claim.RunID)
		if err != nil {
			t.Fatal(err)
		}
		run.Request.Request.EnvironmentSource = runspec.EnvironmentSubmitted
		run.Request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"BUILD_VALUE": value}, strings.Repeat("5", 64))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(run.Request)
		if err != nil {
			t.Fatal(err)
		}
		values, err := json.Marshal(map[string][]byte{"BUILD_VALUE": []byte(value)})
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := cipher.SealBound(secrets.PurposeRunEnvironment, string(claim.Team), claim.RunID, "run-environment", false, true, string(values))
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,submitted_env=? WHERE id=?`, raw, envelope, claim.RunID)
		bootstrap := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
		if got, err := tenant.ReadSubmittedEnvironment(t.Context(), bootstrap); err == nil || got != nil {
			t.Fatal("bootstrap read environment")
		}
		issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: bootstrap.ID})
		if err != nil {
			t.Fatal(err)
		}
		authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
		got, err := tenant.ReadSubmittedEnvironment(t.Context(), authority)
		if err != nil || string(got["BUILD_VALUE"]) != value || len(got) != 1 {
			t.Fatalf("%s lost environment: %v", kind, err)
		}
		table, predicate := "triggers", `id='run'`
		if kind == capability.UnitNode {
			table, predicate = "nodes", `run_id='run'`
		}
		if kind == capability.UnitEval {
			table, predicate = "eval_units", `run_id='run'`
		}
		switch change % 11 {
		case 0:
			tenant.team = "foreign"
		case 1:
			rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET generation=generation+1 WHERE "+predicate)
		case 2:
			rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET claimed_by='foreign' WHERE "+predicate)
		case 3:
			rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET capability_prefix='foreign' WHERE "+predicate)
		case 4:
			*now = now.Add(3 * time.Hour)
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, authority.ID)
		case 6:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
		case 7:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`)
		case 8:
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1 WHERE run_id='run'`)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=1 WHERE id='run'`)
			}
		case 9:
			rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET lease_expires_at=1 WHERE "+predicate)
		case 10:
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET cancel_requested_at=1 WHERE run_id='run'`)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=1 WHERE id='run'`)
			}

		}
		if got, err := tenant.ReadSubmittedEnvironment(t.Context(), authority); err == nil || got != nil {
			t.Fatalf("%s released outside live fence %d", kind, change%11)
		}
	})
}
