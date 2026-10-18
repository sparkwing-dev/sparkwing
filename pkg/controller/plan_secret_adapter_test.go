package controller

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type planSecretProbe struct {
	BoundCipher
	opens int
	t     *testing.T
}

func (p *planSecretProbe) OpenBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, value string) (string, error) {
	p.opens++
	if team != "a" || name != "TOKEN" || scope != "ci" || shared || !masked || value != "sealed-value" {
		p.t.Fatal("plan secret lost its stored binding")
	}
	return "", errors.New("probe decryption refused")
}

func TestPlanSecretOpenerUsesStoreSelectedBinding(t *testing.T) {
	for _, configured := range []bool{false, true} {
		e, tenant := engineRuntimeStore(t)
		engineSQL(t, e.Store, `INSERT INTO secrets(team,name,pipeline,value,masked,principal,created_at,updated_at) VALUES ('a','TOKEN','ci','sealed-value',1,'account',1,1)`)
		probe := &planSecretProbe{t: t}
		server := New(e.Store, nil).WithSecretsCipher(probe)
		if !configured {
			server.WithSecretsCipher(nil)
		}
		owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
		_, err := e.AcceptPlan(t.Context(), owner, controllerwire.PlanSubmission{Plan: []byte(`{"jobs":[]}`), Resolved: runspec.Resolved{SecretArgs: []string{}}})
		var failure *apierr.Error
		want := "plan secret decryption is not configured"
		if configured {
			want = "plan secret decryption failed"
		}
		if !errors.As(err, &failure) || failure.Code != apierr.Unavailable || failure.Message != want || (probe.opens == 1) != configured {
			t.Fatalf("configured=%v opens=%d err=%v", configured, probe.opens, err)
		}
		run, err := tenant.ReadRun(t.Context(), "run")
		if err != nil || run.Spec != nil || len(run.Plan) != 0 {
			t.Fatal("failed secret decryption persisted a plan")
		}
	}
}

func planSecretCipherFixture(t *testing.T) (planSecretCipher, store.Secret) {
	t.Helper()
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	secret := store.Secret{Name: "TOKEN", Pipeline: "ci", Masked: true}
	secret.Value, err = SealSecretValue(cipher, "a", &secret, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	return planSecretCipher{cipher: cipher}, secret
}

func TestPlanSecretOpenerOpensOriginalEnvelopeAndHonorsCancellation(t *testing.T) {
	opener, secret := planSecretCipherFixture(t)
	value, err := opener.OpenPlanSecret(t.Context(), "a", secret)
	if err != nil || value != "private-value" {
		t.Fatalf("opened value differs: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := opener.OpenPlanSecret(ctx, "a", secret); !errors.Is(err, context.Canceled) || value != "" {
		t.Fatal("cancelled opening returned plaintext")
	}
}

func FuzzPlanSecretOpenerCannotOpenRelocatedEnvelope(f *testing.F) {
	f.Add("foreign", uint8(0))
	f.Add("", uint8(4))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		opener, secret := planSecretCipherFixture(t)
		team := store.Team("a")
		switch field % 5 {
		case 0:
			team = store.Team("foreign-" + value)
		case 1:
			secret.Name = "foreign-" + value
		case 2:
			secret.Pipeline = "foreign-" + value
		case 3:
			secret.Masked = false
		case 4:
			secret.Shared = true
		}
		if plain, err := opener.OpenPlanSecret(t.Context(), team, secret); err == nil || plain != "" {
			t.Fatal("relocated envelope returned plaintext")
		}
	})
}

func TestPlanSecretOpenerRejectsSecretIdentifiersInAcceptance(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	opener, secret := planSecretCipherFixture(t)
	New(e.Store, nil).WithSecretsCipher(opener.cipher)
	engineSQL(t, e.Store, `INSERT INTO secrets(team,name,pipeline,value,masked,principal,created_at,updated_at) VALUES ('a','TOKEN','ci',?,1,'account',1,1)`, secret.Value)
	owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
	property := func(input []byte, field uint8) bool {
		if len(input) > 16 {
			input = input[:16]
		}
		identifier := "x-" + hex.EncodeToString(input) + "-private-value"
		job := nodeproto.JobSpec{ID: "build", Steps: []nodeproto.StepSpec{{ID: "run"}}}
		switch field % 4 {
		case 0:
			job.ID = identifier
		case 1:
			job.Steps[0].ID = identifier
		case 2:
			job.Groups = []string{identifier}
		case 3:
			job.Concurrency = []nodeproto.ConcurrencySpec{{Group: identifier}}
		}
		raw, err := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.AcceptPlan(t.Context(), owner, controllerwire.PlanSubmission{Plan: raw, Resolved: runspec.Resolved{SecretArgs: []string{}}})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier || strings.Contains(err.Error(), "private-value") {
			t.Logf("secret-bearing plan returned %v", err)
			return false
		}
		return true
	}
	for field := range uint8(4) {
		if !property(nil, field) {
			t.Fatalf("secret identifier field %d was accepted", field)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.Spec != nil || len(run.Plan) != 0 {
		t.Fatalf("refused plan persisted: %v", err)
	}
	var count int
	if err := e.Store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM nodes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
}

func TestPlanSecretOpenerAllowsValidatedPlan(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	opener, secret := planSecretCipherFixture(t)
	New(e.Store, nil).WithSecretsCipher(opener.cipher)
	engineSQL(t, e.Store, `INSERT INTO secrets(team,name,pipeline,value,masked,principal,created_at,updated_at) VALUES ('a','TOKEN','ci',?,1,'account',1,1)`, secret.Value)
	owner := engineRuntimeClaim(t, e, capability.UnitTrigger)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := runspec.SealRequest(run.Request.Request, runspec.IntakeFacts{RunID: run.ID, AuthorityRef: owner.Principal.Authority})
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,request_hash=? WHERE team='a' AND id='run'`, request, string(sealed.Hash))
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: sealed, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := e.AcceptPlan(t.Context(), owner, controllerwire.PlanSubmission{Declarations: declarations, Plan: body,
		Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil || accepted.Spec.Hash == "" || accepted.PlanHash == "" || strings.Contains(string(accepted.Plan), "private-value") {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	stored, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || stored.Spec == nil || stored.Spec.Hash != accepted.Spec.Hash {
		t.Fatalf("plan did not persist: %v", err)
	}
}
