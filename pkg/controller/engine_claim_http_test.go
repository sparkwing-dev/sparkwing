package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func engineHTTPRoot(t *testing.T, e *EngineController, team, prefix string, grants []capability.Grant) string {
	t.Helper()
	secret := sha256.Sum256([]byte(prefix))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(raw))
	kind := capability.SubjectMachine
	if team == "" {
		kind = capability.SubjectService
	}
	c := capability.Capability{Prefix: prefix, Team: team, Subject: capability.Subject{Kind: kind, ID: prefix},
		Audience: capability.Controller, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone},
		Issuance: capability.Delegate, ReservedPools: []string{}, Repositories: []string{}, IssuedAt: time.Unix(0, 1).UTC()}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	encodedGrants, err := json.Marshal(grants)
	if err != nil {
		t.Fatal(err)
	}
	var storedTeam any
	if team != "" {
		storedTeam = team
	}
	engineSQL(t, e.Store, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,?,?,?,?,'controller',?,'delegate',1,?,'none',?,?,1,?)`,
		hex.EncodeToString(digest[:]), prefix, storedTeam, kind, prefix, encodedGrants, envelope, []byte(`[]`), []byte(`[]`), prefix)
	return raw
}

func engineClaimHTTP(t *testing.T, h http.Handler, raw, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if raw != "" {
		r.Header.Set("Authorization", "Bearer "+raw)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), status)
	}
	return w
}

func engineClaimHTTPFixture(t *testing.T, grants []capability.Grant) (*EngineController, http.Handler, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	engineRuntimeClaim(t, e, capability.UnitTrigger)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	e.Store.SetClaimProfileResolver(engineProfileFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	raw := engineHTTPRoot(t, e, "a", "root", grants)
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	return e, h, raw
}

func TestEngineClaimHTTPDeliversPrivateAwardOnce(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	body := `{"runner_version":"v1.2.3","kinds":["trigger","node"],"labels":["name=forged","class=cloud"],"available_cores":1}`
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim?team=foreign", body, 200)
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	c := response.Claim
	if c == nil || c.Capability == "" || c.Binding.RunID != "run" || c.Binding.NodeID != "job" || c.Runner.Name != "root" || c.Runner.Type != "runner" || c.Execution == nil || c.Request.Request.Team != "a" {
		t.Fatalf("private award fields missing or unbound: %+v", c)
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), raw) {
		t.Fatal("claim response cached or disclosed its parent bearer")
	}
	w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", body, 200)
	if strings.Contains(w.Body.String(), c.Capability) || strings.Contains(w.Body.String(), `"capability"`) {
		t.Fatal("held claim returned another bearer")
	}
	var count int
	if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("awards=%d: %v", count, err)
	}
}

func TestEngineClaimHTTPRefusalsDoNotAward(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	for _, body := range []string{
		`{"runner_version":"v1","kinds":[]}`,
		`{"runner_version":"v1","kinds":["node","trigger"]}`,
		`{"runner_version":"v1","kinds":["trigger","node"]}`,
		`{"runner_version":"v1","kinds":["node","node"]}`,
		`{"runner_version":"v1","kinds":["plan"]}`,
		`{"runner_version":"v1","kinds":["node"],"team":"foreign"}`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/runners/claim", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 403 {
			t.Fatalf("invalid claim returned %d %s", w.Code, w.Body.String())
		}
	}
	engineClaimHTTP(t, h, "", "POST", "/api/v1/runners/claim", `{}`, 401)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"]}`, 403)
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error {
		tenant, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
		if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 401)
	var holder string
	if err := e.Store.DB().QueryRow(`SELECT COALESCE(claimed_by,'') FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&holder); err != nil || holder != "" {
		t.Fatalf("refused request mutated holder %q: %v", holder, err)
	}
}

func TestEngineClaimHTTPLauncherExchangesAndRenewsItsOwnUnit(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	e.Store.SetClaimProfileResolver(engineProfileFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["plan"],"available_cores":1}`, 200)
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Claim == nil || response.Claim.Delivery != capability.DeliveryBootstrap || response.Claim.Runner.Name != "launcher" || response.Claim.Unit != capability.UnitPlan {
		t.Fatalf("launcher award: %+v", response.Claim)
	}
	bootstrap := response.Claim.Capability
	engineClaimHTTP(t, h, bootstrap, "POST", "/api/v1/claims/exchange", `{"run_id":"foreign"}`, 400)
	w = engineClaimHTTP(t, h, bootstrap, "POST", "/api/v1/claims/exchange", `{}`, 200)
	var exchange controllerwire.ExchangeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &exchange); err != nil {
		t.Fatal(err)
	}
	claim := exchange.Claim
	if claim.Capability == "" || claim.Capability == bootstrap || claim.Delivery != capability.DeliveryDirect || !reflect.DeepEqual(claim.Binding, response.Claim.Binding) || !claim.LeaseExpiresAt.Equal(response.Claim.LeaseExpiresAt) {
		t.Fatal("exchange changed the immutable award or did not replace its bootstrap")
	}
	engineClaimHTTP(t, h, bootstrap, "POST", "/api/v1/claims/exchange", `{}`, 401)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, 403)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/claims/foreign/heartbeat", `{"executing":true}`, 403)
	w = engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/claims/"+claim.UnitID+"/heartbeat", `{"executing":true}`, 200)
	var renewal controllerwire.RenewalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &renewal); err != nil || renewal.ExpiresAt.IsZero() {
		t.Fatalf("renewal: %+v, %v", renewal, err)
	}
	for _, path := range []string{"/api/v1/triggers/claim", "/api/v1/nodes/claim", "/api/v1/triggers/run/claim"} {
		engineClaimHTTP(t, h, raw, "POST", path, `{}`, http.StatusNotFound)
	}
}

func FuzzEngineClaimHTTPKindsNeverWidenGrants(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 0})
	f.Fuzz(func(t *testing.T, values []byte) {
		if len(values) > 32 {
			values = values[:32]
		}
		e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
		kinds := make([]string, len(values))
		for i, value := range values {
			kinds[i] = []string{"node", "trigger", "plan", "eval", "unknown"}[int(value)%5]
		}
		body, err := json.Marshal(map[string]any{"runner_version": "v1", "kinds": kinds, "available_cores": 1})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/runners/claim", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, r)
		allowed := len(kinds) == 1 && kinds[0] == "node"
		if allowed && w.Code != 200 || !allowed && (w.Code != 400 && w.Code != 403) {
			t.Fatalf("kinds=%v returned %d %s", kinds, w.Code, w.Body.String())
		}
		if !allowed && strings.Contains(w.Body.String(), `"capability"`) {
			t.Fatal("invalid kind selection disclosed an award")
		}
		var count int
		if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&count); err != nil || !allowed && count != 0 {
			t.Fatalf("invalid selection persisted %d awards: %v", count, err)
		}
	})
}

func TestEngineClaimHTTPAuthenticatesBeforeNamedLookupErrors(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := engineHTTPRoot(t, e, "a", "named", []capability.Grant{capability.TriggersClaim})
	e.Store.SetClaimProfileResolver(engineProfileFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	body := `{"runner_version":"v1","kinds":["trigger"],"available_cores":1}`
	engineClaimHTTP(t, h, "", "POST", "/api/v1/runs/missing/claim", body, 401)
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/missing/claim", body, 404)
	if !strings.Contains(w.Body.String(), fmt.Sprintf(`"code":%q`, apierr.NotFound)) {
		t.Fatalf("missing run: %s", w.Body.String())
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/claim", `{"runner_version":"v1","kinds":["trigger"],"run_id":"foreign"}`, 400)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/claim", body, 200)
}

func TestEngineClaimHTTPDeadCredentialProperty(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	body := `{"runner_version":"v1","kinds":["node"],"available_cores":1}`
	if err := quick.Check(func(secret [32]byte, legacy bool) bool {
		prefix := "swk_"
		if legacy {
			prefix = "swr_"
		}
		unknown := prefix + base64.RawURLEncoding.EncodeToString(secret[:])
		w := engineClaimHTTP(t, h, unknown, "POST", "/api/v1/runners/claim", body, 401)
		return !strings.Contains(w.Body.String(), unknown) && !strings.Contains(w.Body.String(), `"capability"`)
	}, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"compute_start"}`, 403)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", body, 401)
	var count int
	if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dead roots persisted %d awards: %v", count, err)
	}
}

func TestEngineClaimHTTPCommitsRunnerObservations(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	before := time.Now()
	body := `{"runner_version":"v1","kinds":["node"],"available_cores":1,"labels":["tool:git","name=impostor","class=cloud","pool=forged"]}`
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", body, 200)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	runners, err := tenant.ListRegisteredAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, runner := range runners {
		if runner.CapabilityPrefix != "root" {
			continue
		}
		if runner.Name != "root" || runner.Profile.Class != "agent" || len(runner.Profile.ReservedPools) != 0 || !reflect.DeepEqual(runner.Profile.Labels, []string{"tool:git"}) || runner.LastSeen.Before(before) || runner.LastSeen.After(time.Now()) {
			t.Fatalf("poll changed enrollment or lost its observation: %+v", runner)
		}
		return
	}
	t.Fatal("committed machine observation missing")
}
