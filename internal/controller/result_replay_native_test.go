package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func nativeClaimPresentation(t *testing.T, e *engineFixture, a capability.Authorized) string {
	t.Helper()
	secret := sha256.Sum256([]byte("native-" + a.Principal.Capability.Prefix))
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	c := a.Principal.Capability
	c.Digest = sha256.Sum256([]byte(raw))
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE capabilities SET digest=?,authority_json=? WHERE prefix=?`, hex.EncodeToString(c.Digest[:]), envelope, c.Prefix)
	return raw
}

func TestNativeTerminalResultReplayUsesCommittedProof(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	a := engineRuntimeClaim(t, e, capability.UnitNode)
	raw := nativeClaimPresentation(t, e, a)
	h := New(e.Store, nil).Handler()
	path := "/api/v1/runs/run/jobs/job/transition"
	engineClaimHTTP(t, h, raw, "POST", path, `{"to":"running","cause":"start","source_attempt":0}`, 200)
	body := `{"to":"success","cause":"report","source_attempt":1,"result":{"outcome":"success"}}`
	engineClaimHTTP(t, h, raw, "POST", path, body, 200)
	replay := engineClaimHTTP(t, h, raw, "POST", path, body, 200)
	if !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal(replay.Body.String())
	}
	property := func(input []byte) bool {
		changed := body + " " + base64.RawURLEncoding.EncodeToString(input)
		r := httptest.NewRequest("POST", path, strings.NewReader(changed))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 401
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT state_actor FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := quick.Check(func(selector uint8, input []byte) bool {
		var actor store.Actor
		if err := json.Unmarshal(original, &actor); err != nil {
			t.Fatal(err)
		}
		suffix := "other-" + base64.RawURLEncoding.EncodeToString(input)
		switch selector % 8 {
		case 0:
			actor.Result.Actor.ID += suffix
		case 1:
			actor.Result.Generation++
		case 2:
			actor.Result.Attempt++
		case 3:
			actor.Result.Method = "GET"
		case 4:
			actor.Result.Pattern += suffix
		case 5:
			actor.Result.RunID += suffix
		case 6:
			actor.Result.NodeID += suffix
		case 7:
			actor.Result.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte(suffix)))
		}
		encoded, err := json.Marshal(actor)
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, e.Store, `UPDATE nodes SET state_actor=? WHERE team='a' AND run_id='run' AND node_id='job'`, encoded)
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 401
	}, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE nodes SET state_actor=? WHERE team='a' AND run_id='run' AND node_id='job'`, original)
	engineClaimHTTP(t, h, raw, "POST", path, body, 200)
	if err := quick.Check(func(input []byte) bool {
		engineSQL(t, e.Store, `UPDATE node_attempts SET holder=? WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=1`, "other-"+base64.RawURLEncoding.EncodeToString(input))
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 401
	}, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE node_attempts SET holder=? WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=1`, a.Binding.Holder)
	engineSQL(t, e.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	engineClaimHTTP(t, h, raw, "POST", path, body, 401)
}

func TestNativePlanAndFinishReplayKeepDistinctProofs(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	a := engineRuntimeClaim(t, e, capability.UnitTrigger)
	raw := nativeClaimPresentation(t, e, a)
	body := nativePlanBody(t, e, tenant, a)
	h := New(e.Store, nil).Handler()
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", string(body), 200)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", string(body)+" ", 409)
	finish := `{}`
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/finish", finish, 200)
	for _, item := range []struct{ path, body string }{{"/api/v1/runs/run/plan", string(body)}, {"/api/v1/runs/run/finish", finish}} {
		replay := engineClaimHTTP(t, h, raw, "POST", item.path, item.body, 200)
		if !strings.Contains(replay.Body.String(), `"replayed":true`) {
			t.Fatal(replay.Body.String())
		}
		engineClaimHTTP(t, h, raw, "POST", item.path, item.body+" ", 401)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/finish", string(body), 401)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", finish, 401)
	engineSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
	engineSQL(t, e.Store, `UPDATE triggers SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", string(body), 200)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", string(body)+" ", 401)
}

func nativePlanBody(t *testing.T, e *engineFixture, tenant *store.Tenant, a capability.Authorized) []byte {
	t.Helper()
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Dispatch = runspec.Dispatch(run.Dispatch)
	sealed, err := runspec.SealRequest(run.Request.Request, runspec.IntakeFacts{RunID: run.ID, AuthorityRef: a.Principal.Authority, Provenance: run.Request.Provenance})
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,request_hash=? WHERE team='a' AND id='run'`, request, string(sealed.Hash))
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: sealed, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(controllerwire.PlanSubmission{Declarations: declarations, Plan: encodedPlan, Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNativeControllerPlanReplayAfterOwnerHandoff(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "plan-replay", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller' WHERE team='a' AND id='run'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan' WHERE team='a' AND id='run'`)
	engineSQL(t, e.Store, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,1)`, time.Now().Add(time.Hour).UnixNano())
	launcher := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	p, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(launcher)))
	if err != nil {
		t.Fatal(err)
	}
	p.Capability.Repositories = []string{"gh:1"}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: p.Capability})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE capabilities SET repo_scope=?,authority_json=? WHERE prefix='launcher'`, []byte(`["gh:1"]`), envelope)
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := e.Store.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.Store.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest)
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	body := nativePlanBody(t, e, tenant, a)
	h := New(e.Store, nil).Handler()
	engineSQL(t, e.Store, `CREATE TRIGGER refuse_plan_proof BEFORE UPDATE OF result_digest ON capabilities WHEN NEW.result_digest<>'' BEGIN SELECT RAISE(ABORT,'proof write refused'); END`)
	engineClaimHTTP(t, h, issued.Raw, "POST", "/api/v1/runs/run/plan", string(body), 500)
	var count int
	if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team='a' AND id='run' AND spec_json IS NOT NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial plan survived proof failure: count=%d error=%v", count, err)
	}
	engineSQL(t, e.Store, `DROP TRIGGER refuse_plan_proof`)
	engineClaimHTTP(t, h, issued.Raw, "POST", "/api/v1/runs/run/plan", string(body), 200)
	engineClaimHTTP(t, h, issued.Raw, "POST", "/api/v1/runs/run/plan", string(body), 200)
	engineClaimHTTP(t, h, issued.Raw, "POST", "/api/v1/runs/run/plan", string(body)+" ", 401)
}
