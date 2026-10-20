package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type creditsFixture struct {
	srv      *controller.Server
	handler  http.Handler
	console  string
	store    *store.Store
	admin    string
	readonly string
	writer   string
}

func newCreditsFixture(t *testing.T) creditsFixture {
	t.Helper()
	browser := newDeploymentBrowserFixture(t, deploymenttest.Cloud(t, "https://controller.test"), nil)
	st := browser.st
	now := time.Now().UTC()
	admin, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Team: "", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "root"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}, ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	readonly, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "viewer"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.read"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	ctrl := browser.srv.WithOperatorAccounts([]string{"alice"})
	return creditsFixture{
		srv: ctrl, handler: ctrl.Handler(), console: sessionAuth(browser.sessID), store: st, writer: browser.token,
		admin: admin, readonly: readonly,
	}
}

func (f creditsFixture) request(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, dashOrigin+path, reader)
	r.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(token, "Session ") {
		if err := controller.ConsoleFixtureRequest(f.srv, r, sessionIDOf(token)); err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", dashOrigin)
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func creditsRequest(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func TestCredits_RoutesShowGrantAndHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t)

	status, body := f.request(t, http.MethodGet, "/api/v1/credits", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /credits = %d: %s", status, body)
	}
	var state struct {
		BalanceMicro       int64 `json:"balance_micro"`
		RateMicroPerSecond int64 `json:"rate_micro_per_second"`
		GraceSeconds       int64 `json:"grace_seconds"`
		MaxChargeSeconds   int64 `json:"max_charge_seconds"`
		MicroPerCredit     int64 `json:"micro_per_credit"`
		CreditsPerDollar   int64 `json:"credits_per_dollar"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.BalanceMicro != 0 {
		t.Fatalf("fresh balance = %d", state.BalanceMicro)
	}
	if state.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("rate = %d, want %d", state.RateMicroPerSecond, store.DefaultCreditRateMicro)
	}
	if state.GraceSeconds != store.DefaultCreditGraceSeconds {
		t.Fatalf("grace = %d, want %d", state.GraceSeconds, store.DefaultCreditGraceSeconds)
	}
	if state.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("charge cap = %d, want %d", state.MaxChargeSeconds, store.DefaultCreditMaxChargeSeconds)
	}
	if state.MicroPerCredit != store.MicroCreditsPerCredit || state.CreditsPerDollar != store.CreditsPerDollar {
		t.Fatalf("unit constants = %d/%d", state.MicroPerCredit, state.CreditsPerDollar)
	}

	reference := controller.PaidCreditsFixture(t, f.store, teamTenant(t, f.store, store.DefaultTeam), 1000)

	status, body = f.request(t, http.MethodGet, "/api/v1/credits/history", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("history = %d: %s", status, body)
	}
	var history struct {
		Grants []struct {
			Kind        string `json:"kind"`
			AmountMicro int64  `json:"amount_micro"`
			Reference   string `json:"reference"`
			CreatedBy   string `json:"created_by"`
		} `json:"grants"`
		Charges []json.RawMessage `json:"charges"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(history.Grants))
	}
	if history.Grants[0].Reference != reference || history.Grants[0].CreatedBy != "payment_provider" || history.Grants[0].Kind != store.CreditGrantPaid || history.Grants[0].AmountMicro != 1000*store.MicroCreditsPerCent {
		t.Fatalf("grant row = %+v", history.Grants[0])
	}
	if len(history.Charges) != 0 {
		t.Fatalf("charges = %d, want 0", len(history.Charges))
	}

	status, body = f.request(t, http.MethodGet,
		"/api/v1/credits/history?limit=5000", f.readonly, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("history above the limit ceiling = %d: %s", status, body)
	}
	if !bytes.Contains(body, []byte("1000")) {
		t.Fatalf("the refusal does not name the ceiling: %s", body)
	}
}

func creditSettings(t *testing.T, f creditsFixture, method string, body any) (int, creditSettingsView) {
	t.Helper()
	path, auth := "/api/v1/credits/settings", f.readonly
	if method != http.MethodGet {
		path, auth = "/api/v1/operator/credits/settings", f.console
	}
	status, raw := f.request(t, method, path, auth, body)
	var view creditSettingsView
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode settings: %v: %s", err, raw)
		}
	}
	return status, view
}

type creditSettingsView struct {
	RateMicroPerSecond int64            `json:"rate_micro_per_second"`
	RateTable          []creditRateView `json:"rate_table"`
	RateTableSet       bool             `json:"rate_table_set"`
	WarmCPUClassCores  int64            `json:"warm_cpu_class_cores"`
	GraceSeconds       int64            `json:"grace_seconds"`
	MaxChargeSeconds   int64            `json:"max_charge_seconds"`
	MicroPerCredit     int64            `json:"micro_per_credit"`
	CreditsPerDollar   int64            `json:"credits_per_dollar"`
}

type creditRateView struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

func (v creditSettingsView) rateFor(cores int64) int64 {
	for _, entry := range v.RateTable {
		if entry.Cores == cores {
			return entry.MicroPerSecond
		}
	}
	return 0
}

func TestCreditSettings_ReadNeedsRunsReadAndWriteNeedsAdmin(t *testing.T) {
	f := newCreditsFixture(t)

	status, _ := f.request(t, http.MethodGet, "/api/v1/credits/settings", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("read with runs.read = %d, want 200", status)
	}
	status, _ = f.request(t, http.MethodPut, "/api/v1/operator/credits/settings", f.readonly,
		map[string]any{"grace_seconds": 0})
	if status != http.StatusForbidden {
		t.Fatalf("write without admin = %d, want 403", status)
	}
}

func TestCreditSettings_ReadsTheDefaultRateLadder(t *testing.T) {
	f := newCreditsFixture(t)

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	if view.RateTableSet {
		t.Fatal("a controller that set no table reports one")
	}
	want := store.DefaultCreditRateTable(store.DefaultCreditRateMicro)
	if len(view.RateTable) != len(want) {
		t.Fatalf("the settings route named %d classes, want %d", len(view.RateTable), len(want))
	}
	for i, entry := range view.RateTable {
		if entry.Cores != want[i].Cores || entry.MicroPerSecond != want[i].MicroPerSecond {
			t.Fatalf("the %d-core class costs %d, want %+v", entry.Cores, entry.MicroPerSecond, want[i])
		}
	}
}

func TestCreditSettings_TakesTheRateTableAsAListOrAsAnObject(t *testing.T) {
	f := newCreditsFixture(t)

	status, view := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": []map[string]any{
			{"cores": 2, "micro_per_second": 10_000},
			{"cores": 8, "micro_per_second": 36_667},
			{"cores": 4, "micro_per_second": 20_000},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT a list = %d", status)
	}
	if len(view.RateTable) != 3 || view.RateTable[0].Cores != 2 || view.RateTable[2].Cores != 8 {
		t.Fatalf("rate table = %+v, want three classes smallest first", view.RateTable)
	}
	if view.RateMicroPerSecond != 20_000 {
		t.Fatalf("single rate = %d, want the four-core price 20000", view.RateMicroPerSecond)
	}

	status, view = creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 11_000, "4": 22_000},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT an object = %d", status)
	}
	if view.rateFor(2) != 11_000 || view.rateFor(4) != 22_000 || len(view.RateTable) != 2 {
		t.Fatalf("rate table after the object write = %+v", view.RateTable)
	}
	if view.GraceSeconds != store.DefaultCreditGraceSeconds {
		t.Fatalf("writing the table moved the grace period to %d", view.GraceSeconds)
	}
}

func TestCreditSettings_RefusesARateTableTheLedgerCannotPrice(t *testing.T) {
	f := newCreditsFixture(t)

	for name, body := range map[string]map[string]any{
		"no class":   {"rate_table": []map[string]any{}},
		"null table": {"rate_table": nil},
		"zero cores": {"rate_table": []map[string]any{{"cores": 0, "micro_per_second": 1}}},
		"zero rate":  {"rate_table": []map[string]any{{"cores": 2, "micro_per_second": 0}}},
		"a class twice": {"rate_table": []map[string]any{
			{"cores": 2, "micro_per_second": 1}, {"cores": 2, "micro_per_second": 2},
		}},
		"a key that is not a core count": {"rate_table": map[string]int64{"large": 1}},
		"neither a list nor an object":   {"rate_table": "2=10000"},
	} {
		if status, _ := creditSettings(t, f, http.MethodPut, body); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}

	status, view := creditSettings(t, f, http.MethodGet, nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d", status)
	}
	want := store.DefaultCreditRateTable(store.DefaultCreditRateMicro)
	for i, entry := range view.RateTable {
		if entry.MicroPerSecond != want[i].MicroPerSecond {
			t.Fatalf("a refused write priced the %d-core class at %d", entry.Cores, entry.MicroPerSecond)
		}
	}
}

func TestCreditSettings_RefusesTheScalarOnceATableExists(t *testing.T) {
	f := newCreditsFixture(t)

	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("PUT the table = %d", status)
	}
	status, body := f.request(t, http.MethodPut, "/api/v1/operator/credits/settings", f.console,
		map[string]any{"rate_micro_per_second": 30_000})
	if status != http.StatusBadRequest {
		t.Fatalf("PUT the scalar = %d, want 400", status)
	}
	if !strings.Contains(string(body), "rate_table") {
		t.Fatalf("the refusal does not name the table: %s", body)
	}
	_, view := creditSettings(t, f, http.MethodGet, nil)
	if view.RateMicroPerSecond != 20_000 || !view.RateTableSet {
		t.Fatalf("the refused write moved the settings: %+v", view)
	}

	// safety: naming both would let the scalar override the table's own
	// four-core entry, so the caller is told to name one.
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_micro_per_second": 21_000,
		"rate_table":            map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusBadRequest {
		t.Fatalf("PUT both = %d, want 400", status)
	}
}
