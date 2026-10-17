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
		tenant.s.SetRunEnvironmentCipher(cipher)
		value := "submitted-value-" + string(raw)
		if strings.ContainsRune(value, 0) {
			return
		}
		request.SubmittedEnv = map[string][]byte{"APP_NOTE": []byte(value), "APP_EMPTY": {}}
		request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"APP_NOTE": value, "APP_EMPTY": ""})
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
		owner.Grants = []capability.Grant{capability.SourceRead, capability.TriggerHost}
		owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
		persistPlanOwner(t, tenant.s, owner)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET status='claimed',claimed_by='host',generation=1,lease_expires_at=?,capability_prefix=? WHERE id=?`, now.Add(time.Minute).UnixNano(), owner.Prefix, admitted.RunID)
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
	tenant.s.SetRunEnvironmentCipher(cipher)
	request.SubmittedEnv = map[string][]byte{"BUILD_NOTE": []byte("retained-private-value")}
	request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"BUILD_NOTE": "retained-private-value"})
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
			tenant.s.SetRunEnvironmentCipher(cipher)
			name, value := "BUILD_NOTE", "private-value"
			if problem == "credential" {
				name = "BUILD_TOKEN"
			}
			if problem == "reserved" {
				name = "SPARKWING_RUN_ID"
			}
			request.SubmittedEnv = map[string][]byte{name: []byte(value)}
			request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{name: value})
			if err != nil {
				t.Fatal(err)
			}
			request.Request.Options.SubmitEnvAllow.Value = []string{name}
			switch problem {
			case "missing":
				request.SubmittedEnv = nil
			case "changed":
				request.SubmittedEnv[name] = []byte("different")
			case "extra":
				request.SubmittedEnv["EXTRA"] = []byte("extra-value")
			case "unlisted":
				request.Request.Options.SubmitEnvAllow.Value = nil
			case "no_cipher":
				tenant.s.SetRunEnvironmentCipher(nil)
			}
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil || strings.Contains(err.Error(), value) {
				t.Fatal("invalid private environment admitted or disclosed")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
		})
	}
}
