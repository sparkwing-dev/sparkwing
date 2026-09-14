package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRenderCreditState(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := renderCreditState(&buf, creditStateResp{
		BalanceMicro:       987_500_000,
		GrantedMicro:       1_000_000_000,
		ChargedMicro:       12_500_000,
		RateMicroPerSecond: store.DefaultCreditRateMicro,
		GraceSeconds:       60,
		MaxChargeSeconds:   store.DefaultCreditMaxChargeSeconds,
		BurnWindowSeconds:  86400,
		BurnMicro:          12_500_000,
		MicroPerCredit:     store.MicroCreditsPerCredit,
		CreditsPerDollar:   store.CreditsPerDollar,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"987.50 credits", "1000.00 credits", "0.020000 credits", "BURN (24h)", "GRACE", "CHARGE CAP"} {
		if !strings.Contains(out, want) {
			t.Errorf("credit state output is missing %q:\n%s", want, out)
		}
	}
}

func TestCreditHistoryRowsMergeNewestFirst(t *testing.T) {
	t.Parallel()
	rows := creditHistoryRows(creditHistoryResp{
		Grants: []creditGrantResp{
			{ID: "grant-1", Kind: "paid", AmountMicro: 1_000_000_000, Reference: "pay_1", CreatedAt: 100},
		},
		Charges: []creditChargeResp{
			{
				ID: "charge-1", RunID: "run-a", NodeID: "build", TokenPrefix: "swr_x",
				Kind: store.CreditChargeUsage, Seconds: 30, AmountMicro: 600_000, ChargedAt: 200,
			},
			{
				ID: "charge-2", RunID: "run-a", NodeID: "build", TokenPrefix: "swr_x",
				Kind: store.CreditChargeReservation, Seconds: 60, AmountMicro: 1_200_000, ChargedAt: 150,
			},
			{
				ID: "charge-3", RunID: "run-a", NodeID: "build", TokenPrefix: "swr_x",
				Kind: store.CreditChargeRefund, Seconds: -20, AmountMicro: -400_000, ChargedAt: 300,
			},
		},
	})
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if rows[0].Type != store.CreditChargeRefund {
		t.Fatalf("newest row = %q, want the refund", rows[0].Type)
	}
	if rows[0].AmountMicro != 400_000 {
		t.Fatalf("refund renders %d, want credits coming back", rows[0].AmountMicro)
	}
	if rows[1].Type != store.CreditChargeUsage || rows[1].AmountMicro != -600_000 {
		t.Fatalf("usage row = %+v, want a negative amount", rows[1])
	}
	if rows[2].Type != store.CreditChargeReservation || rows[2].AmountMicro != -1_200_000 {
		t.Fatalf("reservation row = %+v", rows[2])
	}
	if rows[3].Type != creditGrantRowType || rows[3].AmountMicro != 1_000_000_000 {
		t.Fatalf("grant row = %+v", rows[3])
	}

	var buf bytes.Buffer
	if err := renderCreditHistory(&buf, rows); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"usage", "reservation", "refund", "grant", "run-a/build", "ref=pay_1", "-0.60", "0.40"} {
		if !strings.Contains(out, want) {
			t.Errorf("history output is missing %q:\n%s", want, out)
		}
	}
}

// A charge from a controller that predates the kind column still renders.
func TestCreditHistoryRowsDefaultTheChargeKind(t *testing.T) {
	t.Parallel()
	rows := creditHistoryRows(creditHistoryResp{
		Charges: []creditChargeResp{{ID: "charge-1", Seconds: 3, AmountMicro: 60_000, ChargedAt: 10}},
	})
	if len(rows) != 1 || rows[0].Type != store.CreditChargeUsage {
		t.Fatalf("rows = %+v, want one usage row", rows)
	}
}

func TestRenderCreditHistoryEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderCreditHistory(&buf, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "(no credit movements)") {
		t.Fatalf("empty history output = %q", buf.String())
	}
}

// The credit verbs speak to the controller over the shared token helpers, so
// this drives those helpers against the real routes rather than a stub.
func TestCreditsCLIWireMatchesTheController(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	admin, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	_, runnerTok, err := st.CreateToken("pool", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("runner token: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	if _, err := tokensPost(srv.URL, admin, "/api/v1/credits/grants", map[string]any{
		"kind":         store.CreditGrantPaid,
		"amount_micro": int64(1000) * store.MicroCreditsPerCredit,
		"reference":    "pay_42",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	raw, err := tokensGet(srv.URL, admin, "/api/v1/credits")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var state creditStateResp
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if state.BalanceMicro != int64(1000)*store.MicroCreditsPerCredit {
		t.Fatalf("balance = %d", state.BalanceMicro)
	}
	if state.MicroPerCredit != store.MicroCreditsPerCredit {
		t.Fatalf("micro_per_credit = %d", state.MicroPerCredit)
	}

	raw, err = tokensGet(srv.URL, admin, "/api/v1/credits/history"+url("").with("limit", "10").encode())
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var history creditHistoryResp
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	rows := creditHistoryRows(history)
	if len(rows) != 1 || rows[0].Type != "grant" || rows[0].Reference != "pay_42" {
		t.Fatalf("history rows = %+v", rows)
	}

	if _, err := tokensPost(srv.URL, admin,
		"/api/v1/tokens/"+runnerTok.Prefix+"/metered", map[string]any{"metered": true}); err != nil {
		t.Fatalf("set-metered: %v", err)
	}
	raw, err = tokensGet(srv.URL, admin, "/api/v1/tokens")
	if err != nil {
		t.Fatalf("tokens list: %v", err)
	}
	var listed struct {
		Tokens []tokenListItem `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode tokens: %v", err)
	}
	found := false
	for _, item := range listed.Tokens {
		if item.Prefix == runnerTok.Prefix {
			found = true
			if !item.Metered {
				t.Fatal("tokens list lost the metering marker")
			}
		}
	}
	if !found {
		t.Fatalf("tokens list does not carry %s", runnerTok.Prefix)
	}
	var table bytes.Buffer
	if err := renderTokensTable(&table, listed.Tokens); err != nil {
		t.Fatalf("render tokens: %v", err)
	}
	if !strings.Contains(table.String(), "METERED") {
		t.Fatalf("tokens table lost the METERED column:\n%s", table.String())
	}
}

func TestCreditGrantAmountRule(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		kind     string
		amount   int64
		reverses string
		ok       bool
	}{
		"a paid grant":                     {store.CreditGrantPaid, 1000, "", true},
		"a paid grant of nothing":          {store.CreditGrantPaid, 0, "", false},
		"a paid grant that reverses":       {store.CreditGrantPaid, 1000, "pay_1", false},
		"a reversal":                       {store.CreditGrantReversal, -1000, "pay_1", true},
		"a reversal that adds credits":     {store.CreditGrantReversal, 1000, "pay_1", false},
		"a reversal that names no payment": {store.CreditGrantReversal, -1000, "", false},
	} {
		err := creditGrantAmountRule(tc.kind, tc.amount, tc.reverses)
		if tc.ok && err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCreditHistoryRowNamesTheReversedPayment(t *testing.T) {
	t.Parallel()
	rows := creditHistoryRows(creditHistoryResp{
		Grants: []creditGrantResp{{
			ID: "grant-r", Kind: store.CreditGrantReversal, AmountMicro: -5 * store.MicroCreditsPerCredit,
			Reference: "re_1", Reverses: "pay_1", CreatedBy: "billing", CreatedAt: 100,
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the reversal", rows)
	}
	detail := creditRowDetail(rows[0])
	for _, want := range []string{"reversal", "ref=re_1", "reverses=pay_1"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q is missing %q", detail, want)
		}
	}
}

func TestRenderCreditStateShowsReversals(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderCreditState(&buf, creditStateResp{
		BalanceMicro:   600_000_000,
		GrantedMicro:   1_000_000_000,
		ReversedMicro:  400_000_000,
		MicroPerCredit: store.MicroCreditsPerCredit,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "REVERSED") || !strings.Contains(buf.String(), "400.00 credits") {
		t.Errorf("credit state output does not report the reversal:\n%s", buf.String())
	}
}

func TestRenderCreditSettings(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := renderCreditSettings(&buf, creditSettingsResp{
		RateMicroPerSecond: store.DefaultCreditRateMicro,
		GraceSeconds:       0,
		MaxChargeSeconds:   store.DefaultCreditMaxChargeSeconds,
		MicroPerCredit:     store.MicroCreditsPerCredit,
		CreditsPerDollar:   store.CreditsPerDollar,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"0.020000 credits", "20000 micro", "GRACE", "0s after", "CHARGE CAP", "30s"} {
		if !strings.Contains(out, want) {
			t.Errorf("credit settings output is missing %q:\n%s", want, out)
		}
	}
}

func creditSettingsFlagSet(t *testing.T, args []string) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	fs.Int64("rate-micro", 0, "")
	fs.Int64("grace-seconds", 0, "")
	fs.Int64("max-charge-seconds", 0, "")
	fs.String("rate-table", "", "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs
}

func TestCreditSettingsBodyCarriesOnlyTheFlagsGiven(t *testing.T) {
	t.Parallel()
	fs := creditSettingsFlagSet(t, []string{"--grace-seconds", "0"})
	body, err := creditSettingsBody(fs, 0, 0, 0, "")
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("body = %v, want grace alone", body)
	}
	if body["grace_seconds"] != int64(0) {
		t.Fatalf("grace_seconds = %v", body["grace_seconds"])
	}

	fs = creditSettingsFlagSet(t, nil)
	body, err = creditSettingsBody(fs, 0, 0, 0, "")
	if err != nil {
		t.Fatalf("empty body: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("a flagless call built %v, so it would write instead of read", body)
	}
}

func TestCreditSettingsBodyRefusesValuesTheLedgerCannotPrice(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"rate at zero":   {"--rate-micro", "0"},
		"negative grace": {"--grace-seconds", "-1"},
		"cap under the heartbeat interval": {
			"--max-charge-seconds", strconv.FormatInt(store.MinCreditMaxChargeSeconds-1, 10),
		},
		"a rate table entry without a rate":      {"--rate-table", "2"},
		"a rate table rate that is not a number": {"--rate-table", "2=cheap"},
		"an empty rate table":                    {"--rate-table", ","},
	} {
		fs := creditSettingsFlagSet(t, args)
		rate, _ := fs.GetInt64("rate-micro")
		grace, _ := fs.GetInt64("grace-seconds")
		maxCharge, _ := fs.GetInt64("max-charge-seconds")
		table, _ := fs.GetString("rate-table")
		if _, err := creditSettingsBody(fs, rate, grace, maxCharge, table); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCreditSettingsCLIWireMatchesTheController(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	admin, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	raw, err := tokensPut(srv.URL, admin, "/api/v1/credits/settings",
		map[string]any{"grace_seconds": 0})
	if err != nil {
		t.Fatalf("put settings: %v", err)
	}
	var view creditSettingsResp
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.GraceSeconds != 0 {
		t.Fatalf("grace = %d, want 0", view.GraceSeconds)
	}
	if view.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("rate = %d, want the default", view.RateMicroPerSecond)
	}

	raw, err = tokensGet(srv.URL, admin, "/api/v1/credits/settings")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	view = creditSettingsResp{}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.GraceSeconds != 0 || view.MaxChargeSeconds != store.DefaultCreditMaxChargeSeconds {
		t.Fatalf("settings read back = %+v", view)
	}
	var plain bytes.Buffer
	if err := writeCreditSettingsPlain(&plain, view); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if !strings.Contains(plain.String(), "grace_seconds\t0\n") {
		t.Fatalf("plain output = %q", plain.String())
	}
}

func TestCreditSettingsBodyCarriesTheRateTableAsPairs(t *testing.T) {
	t.Parallel()
	fs := creditSettingsFlagSet(t, []string{"--rate-table", "2=10000, 8=36667"})
	table, _ := fs.GetString("rate-table")
	body, err := creditSettingsBody(fs, 0, 0, 0, table)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	pairs, ok := body["rate_table"].(map[string]int64)
	if !ok {
		t.Fatalf("rate_table = %#v, want CORES=MICRO pairs", body["rate_table"])
	}
	if len(pairs) != 2 || pairs["2"] != 10_000 || pairs["8"] != 36_667 {
		t.Fatalf("rate_table = %v", pairs)
	}
}

func TestRenderCreditSettingsNamesEveryClass(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := renderCreditSettings(&buf, creditSettingsResp{
		RateMicroPerSecond: store.DefaultCreditRateMicro,
		RateTable: []creditRateResp{
			{Cores: 2, MicroPerSecond: 10_000},
			{Cores: 8, MicroPerSecond: 36_667},
		},
		MaxChargeSeconds: store.DefaultCreditMaxChargeSeconds,
		MicroPerCredit:   store.MicroCreditsPerCredit,
		CreditsPerDollar: store.CreditsPerDollar,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"2-CORE", "0.010000 credits", "8-CORE", "36667 micro"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings output is missing %q:\n%s", want, out)
		}
	}
}

func TestCreditHistoryRowsCarryTheClassAndRateEachChargeWasBilledAt(t *testing.T) {
	t.Parallel()
	rows := creditHistoryRows(creditHistoryResp{
		Charges: []creditChargeResp{
			{
				ID: "charge-1", RunID: "run-a", NodeID: "build", TokenPrefix: "swr_x",
				Kind: store.CreditChargeUsage, Seconds: 10, AmountMicro: 366_670,
				CPUClassCores: 8, RateMicroPerSecond: 36_667, ChargedAt: 200,
			},
			{
				ID: "charge-2", RunID: "run-b", NodeID: "build", TokenPrefix: "swr_x",
				Kind: store.CreditChargeUsage, Seconds: 10, AmountMicro: 200_000, ChargedAt: 100,
			},
		},
	})
	if rows[0].CPUClassCores != 8 || rows[0].RateMicroPerSecond != 36_667 {
		t.Fatalf("classed row = %+v", rows[0])
	}
	if rows[1].CPUClassCores != 0 {
		t.Fatalf("a charge written before the rate table reads class %d", rows[1].CPUClassCores)
	}

	var buf bytes.Buffer
	if err := renderCreditHistory(&buf, rows); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "class=8c rate=36667") {
		t.Errorf("history output does not name the class and rate:\n%s", out)
	}
	if strings.Contains(out, "class=0c") {
		t.Errorf("history output names a class on a row that carries none:\n%s", out)
	}
}

func TestCreditSettingsBodyRefusesARateTableThatPricesAClassTwice(t *testing.T) {
	t.Parallel()
	fs := creditSettingsFlagSet(t, []string{"--rate-table", "2=10000,2=20000"})
	table, _ := fs.GetString("rate-table")
	if _, err := creditSettingsBody(fs, 0, 0, 0, table); err == nil {
		t.Fatal("--rate-table accepted the same class twice")
	}
}

// The flat ladder an unset table prints reads like a priced one, so the output
// says which of the two it is.
func TestRenderCreditStateSaysWhenNoRateTableIsSet(t *testing.T) {
	t.Parallel()
	var unset, set bytes.Buffer
	state := creditStateResp{
		RateMicroPerSecond: store.DefaultCreditRateMicro,
		RateTable:          []creditRateResp{{Cores: 2, MicroPerSecond: store.DefaultCreditRateMicro}},
		MicroPerCredit:     store.MicroCreditsPerCredit,
	}
	if err := renderCreditState(&unset, state); err != nil {
		t.Fatalf("render: %v", err)
	}
	state.RateTableSet = true
	if err := renderCreditState(&set, state); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(unset.String(), "not set") {
		t.Errorf("an unset table is not called out:\n%s", unset.String())
	}
	if !strings.Contains(set.String(), "set by the operator") {
		t.Errorf("a set table is not called out:\n%s", set.String())
	}
}
