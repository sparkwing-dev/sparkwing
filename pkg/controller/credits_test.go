package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// safety: a node whose plan pins no cpu resolves to one core, which the
// smallest class of the default rate table covers, so that is the price these
// fixtures are billed at.
var unpinnedNodeRateMicro = store.DefaultCreditRateTable(store.DefaultCreditRateMicro).RateFor(1)

type creditsFixture struct {
	url      string
	store    *store.Store
	admin    string
	runner   string
	prefix   string
	readonly string
}

func newCreditsFixture(t *testing.T, metered bool) creditsFixture {
	return newCreditsFixtureWithLicense(t, metered, license.FeatureMetering)
}

func newCreditsFixtureWithLicense(t *testing.T, metered bool, feature string) creditsFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	runner, runnerTok, err := st.CreateTokenWith(context.Background(), "pool", store.TokenKindRunner,
		[]string{"nodes.claim", "triggers.claim", "runs.state", "runs.read"},
		0, now, store.TokenOptions{Metered: metered})
	if err != nil {
		t.Fatalf("runner token: %v", err)
	}
	readonly, _, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{"runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	ctrl := controller.New(st, nil)
	controller.ConfigureConsoleFixture(ctrl)
	if feature != "" {
		pub, priv := licensetest.NewKey(t)
		raw := licensetest.Sign(t, priv, licensetest.Terms{
			Features: []string{feature}, IssuedTo: "test",
			IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		})
		ctrl.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
	}
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)
	return creditsFixture{
		url: srv.URL, store: st,
		admin: admin, runner: runner, prefix: runnerTok.Prefix, readonly: readonly,
	}
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

func creditsRequestWithHeader(
	t *testing.T, method, url, token string, body any,
) (int, []byte, http.Header) {
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
	return resp.StatusCode, out, resp.Header
}

func setNodeChargeWindow(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE nodes SET credit_charged_through = ?,
		 execution_started_at = COALESCE(execution_started_at, ?)
		 WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), at.Add(-store.MinBillableSeconds*time.Second).UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("rewind the charge window: %v", err)
	}
}

func ageExhaustionStamp(t *testing.T, st *store.Store, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE teams SET credit_exhausted_at = ? WHERE name = ?`,
		at.UnixNano(), string(store.DefaultTeam)); err != nil {
		t.Fatalf("age the exhaustion stamp: %v", err)
	}
}

// safety: the grace clock a cancellation runs on is the node's own, so a test
// reaches the deadline by moving that instant rather than the ledger stamp.
func ageNodeExhaustionAnchor(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE nodes SET credit_exhausted_anchor = ? WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), runID, nodeID); err != nil {
		t.Fatalf("age the node's exhaustion anchor: %v", err)
	}
}

func creditChargesOfKind(charges []store.CreditCharge, kind string) []store.CreditCharge {
	var out []store.CreditCharge
	for _, c := range charges {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func TestCredits_RoutesShowGrantAndHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, false)

	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.readonly, nil)
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

	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_9", CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}

	status, body = creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/history", f.readonly, nil)
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
	if history.Grants[0].Reference != "pay_9" || history.Grants[0].CreatedBy != "test" {
		t.Fatalf("grant row = %+v", history.Grants[0])
	}
	if len(history.Charges) != 0 {
		t.Fatalf("charges = %d, want 0", len(history.Charges))
	}

	status, body = creditsRequest(t, http.MethodGet,
		f.url+"/api/v1/credits/history?limit=5000", f.readonly, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("history above the limit ceiling = %d: %s", status, body)
	}
	if !bytes.Contains(body, []byte("1000")) {
		t.Fatalf("the refusal does not name the ceiling: %s", body)
	}
}

func creditSettings(t *testing.T, f creditsFixture, method string, body any) (int, creditSettingsView) {
	t.Helper()
	status, raw := creditsRequest(t, method, f.url+"/api/v1/credits/settings", f.admin, body)
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
	f := newCreditsFixture(t, false)

	status, _ := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/settings", f.readonly, nil)
	if status != http.StatusOK {
		t.Fatalf("read with runs.read = %d, want 200", status)
	}
	status, _ = creditsRequest(t, http.MethodPut, f.url+"/api/v1/credits/settings", f.readonly,
		map[string]any{"grace_seconds": 0})
	if status != http.StatusForbidden {
		t.Fatalf("write without admin = %d, want 403", status)
	}
}

// An installation that never set a table reads one price for every class, so
// its bill is what the single rate charged on its own.
func TestCreditSettings_ReadsTheDefaultRateLadder(t *testing.T) {
	f := newCreditsFixture(t, false)

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
	f := newCreditsFixture(t, false)

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
	f := newCreditsFixture(t, false)

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

// With a table stored the scalar is the four-core entry under another name, so
// writing it alone is refused and the caller is told what to write instead.
func TestCreditSettings_RefusesTheScalarOnceATableExists(t *testing.T) {
	f := newCreditsFixture(t, false)

	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("PUT the table = %d", status)
	}
	status, body := creditsRequest(t, http.MethodPut, f.url+"/api/v1/credits/settings", f.admin,
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

// A claim for a node the table cannot price fails the node with the reason, so
// the run stops instead of every poller retrying it forever.
func TestCredits_AClaimAboveTheLargestClassFailsTheNode(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-huge", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":96}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-huge", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-huge", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60})
	if status != http.StatusConflict {
		t.Fatalf("claim = %d: %s", status, body)
	}
	if !strings.Contains(string(body), controller.UnpricedCPUClassCode) {
		t.Fatalf("the refusal carries no code: %s", body)
	}
	node, err := f.store.GetNode(ctx, "run-huge", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Outcome != "failed" || node.FailureReason != store.FailureUnpricedCPUClass {
		t.Fatalf("node = %s/%s, want a failure naming the unpriced class", node.Outcome, node.FailureReason)
	}
}

// A claim that asserts its own cpu changes nothing: the class comes from the
// plan the controller holds.
func TestCredits_AClaimsOwnCPUFigureDoesNotLowerTheBill(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "4": 20_000, "16": 70_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":16}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	// safety: the claim surface carries no cpu figure at all, so a body that
	// asserts one is refused rather than quietly ignored.
	status, _ := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{
			"holder_id": "pod-1", "lease_secs": 60,
			"capacity": map[string]any{"max_concurrent": 1, "active_claims": 0, "cores": 0.01},
		})
	if status != http.StatusBadRequest {
		t.Fatalf("a claim asserting its own cpu = %d, want 400", status)
	}

	// safety: a sixteen-core node is above the warm class, so it is awarded by
	// the targeted claim its own Kubernetes node makes.
	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 16 || charges[0].RateMicroPerSecond != 70_000 {
		t.Fatalf("reservation = %+v, want the 16-core class the plan asked for", charges)
	}
}

// The class the claim response carries is the class the charge row bills,
// whichever ladder the operator priced.
func TestCredits_TheClaimResponseCarriesTheBilledClass(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if status, _ := creditSettings(t, f, http.MethodPut, map[string]any{
		"rate_table": map[string]int64{"2": 10_000, "64": 270_000},
	}); status != http.StatusOK {
		t.Fatalf("set the rate table = %d", status)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":3}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	var claimed store.Node
	if err := json.Unmarshal(body, &claimed); err != nil {
		t.Fatalf("decode the claim: %v: %s", err, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 64 {
		t.Fatalf("reservation = %+v, want the 64-core class this ladder prices", charges)
	}
	if claimed.CreditCPUClassCores != charges[0].CPUClassCores {
		t.Fatalf("claim says class %d, charge row says %d",
			claimed.CreditCPUClassCores, charges[0].CPUClassCores)
	}
	if want := store.CPUClassMemoryBytes(64); claimed.CreditCPUClassMemoryBytes != want {
		t.Fatalf("claim memory = %d, want %d", claimed.CreditCPUClassMemoryBytes, want)
	}
}

func TestCredits_QuarterCorePinReservesTwoCoreMinimum(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay-quarter", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-quarter", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":0.25}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-quarter", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-quarter", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-quarter/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 2 ||
		charges[0].RateMicroPerSecond != 9_000 ||
		charges[0].AmountMicro != 9_000*store.MinBillableSeconds {
		t.Fatalf("reservation = %+v, want the minimum billable seconds at the two-core class", charges)
	}
}

// Naming a node is not a way around the ladder: a claimant outside the
// class-routed path is held to the warm class.
func TestCredits_ANamedClaimThatDoesNotSizeToClassIsRefused(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":8}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "runner:warm", "lease_secs": 60})
	if status != http.StatusConflict {
		t.Fatalf("named claim without sizes_to_class = %d: %s", status, body)
	}
	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("named claim that sizes to the class = %d: %s", status, body)
	}
}

// A node above the warm class is not in the queue a warm runner polls, so it
// waits for a Kubernetes node sized to its class.
func TestCredits_AWarmClaimPassesOverAClassAboveTheWarmPool(t *testing.T) {
	f := newCreditsFixture(t, true)
	ctx := context.Background()
	if _, err := teamTenant(t, f.store, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent, Reference: "pay_1", CreatedBy: "root"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := teststore.ForTeam(t, f.store, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: "run-big", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		PlanSnapshot: []byte(`{"nodes":[{"id":"build","modifiers":{"res_cores":8}}]}`),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: "run-big", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, "run-big", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	status, body := creditsRequest(t, http.MethodPost, f.url+"/api/v1/nodes/claim", f.runner,
		map[string]any{"holder_id": "pod-1", "lease_secs": 60})
	if status != http.StatusNoContent {
		t.Fatalf("warm claim = %d: %s", status, body)
	}
	node, err := f.store.GetNode(ctx, "run-big", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Claimed || node.Outcome != "" {
		t.Fatalf("node = claimed %t outcome %q, want it still queued", node.Claimed, node.Outcome)
	}

	status, body = creditsRequest(t, http.MethodPost,
		f.url+"/api/v1/runs/run-big/nodes/build/claim", f.runner,
		map[string]any{"holder_id": "k8s-job:build", "lease_secs": 60, "sizes_to_class": true})
	if status != http.StatusOK {
		t.Fatalf("targeted claim = %d: %s", status, body)
	}
	charges, err := teamTenant(t, f.store, store.DefaultTeam).ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 1 || charges[0].CPUClassCores != 8 {
		t.Fatalf("reservation = %+v, want the 8-core class", charges)
	}
}
