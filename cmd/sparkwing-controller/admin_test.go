package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestAdminNeedsATeamAndAReason(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	for _, args := range [][]string{
		nil,
		{"grant"},
		{"freeze", "--reason", "abuse"},
		{"freeze", "--team", "default"},
		{"freeze", "--team", "default", "--reason", "   "},
		{"freeze", "--team", "default", "--reason", strings.Repeat("x", 501)},
		{"freeze", "--team", "default", "--reason", "abuse", "--hold", "dp_1"},
		{"grant", "--team", "default", "--reason", "QA"},
		{"grant", "--team", "default", "--reason", "QA", "--cents", "0"},
		{"grant", "--team", "default", "--reason", "QA", "--cents", "-5"},
		{"grant", "--team", "default", "--cents", "500"},
		{"grant", "--team", "default", "--reason", "QA", "--cents", "500", "--hold", "dp_1"},
		{"grant", "--team", "default", "--reason", "QA", "--cents", "500", "--key", "  "},
		{"grant", "--team", "no-such-team", "--reason", "QA", "--cents", "500"},
		{"freeze", "--team", "default", "--reason", "abuse", "--cents", "500"},
		{"unfreeze", "--team", "default", "--reason", "abuse", "--key", "k"},
	} {
		if err := runAdmin(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Errorf("admin %q succeeded, want a refusal", args)
		}
	}
}

// The break-glass command holds and releases a team through the store calls
// the console makes, so each action writes the console's business event, with
// the command as its actor, and an audit line.
func TestAdminFreezesAndReleasesWithTheConsolesRecords(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	ctx := context.Background()
	st := openState(t)
	if _, err := teststore.SQL(t, st).ExecContext(ctx, "INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at,cause) VALUES (?,?,?,?,?,'dispute')", "dp_1", store.DefaultTeam, "", "chargeback", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	type holds struct {
		Team   string   `json:"team"`
		Frozen bool     `json:"frozen"`
		Holds  []string `json:"holds"`
	}
	admin := func(args ...string) (holds, string) {
		t.Helper()
		var out, audit bytes.Buffer
		if err := runAdmin(args, &out, &audit); err != nil {
			t.Fatalf("admin %q: %v", args, err)
		}
		var h holds
		if err := json.Unmarshal(out.Bytes(), &h); err != nil {
			t.Fatalf("admin %q printed %q: %v", args, out.String(), err)
		}
		return h, audit.String()
	}

	frozen, audit := admin("freeze", "--team", "default", "--reason", "console down, abuse in progress")
	if frozen.Team != "default" || !frozen.Frozen || len(frozen.Holds) != 2 {
		t.Fatalf("after freeze = %+v", frozen)
	}
	for _, want := range []string{`"msg":"audit"`, `"principal_kind":"break-glass"`, `"target_team":"default"`, `"command":"sparkwing-controller admin freeze"`} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit line %q lacks %s", audit, want)
		}
	}
	for _, blank := range []string{"", "   "} {
		if err := runAdmin([]string{"unfreeze", "--team", "default", "--reason", "x", "--hold", blank}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Errorf("unfreeze --hold %q succeeded, want a refusal", blank)
		}
	}
	if released, _ := admin("unfreeze", "--team", "default", "--reason", "abuse stopped"); !slices.Equal(released.Holds, []string{"dp_1"}) {
		t.Fatalf("after unfreeze = %+v, want only the dispute's hold left", released)
	}
	if err := runAdmin([]string{"unfreeze", "--team", "default", "--reason", "won", "--hold", "dp_9"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("releasing a hold the team does not hold succeeded")
	}
	if released, _ := admin("unfreeze", "--team", "default", "--reason", "dispute won", "--hold", "dp_1"); released.Frozen || len(released.Holds) != 0 {
		t.Fatalf("after releasing the dispute = %+v", released)
	}

	st = openState(t)
	defer func() { _ = st.Close() }()
	tn, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	events, err := tn.BusinessEvents(ctx, "", 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range events {
		if ev.Kind == store.BusinessEventTeamFrozen && ev.Actor == "" {
			continue
		}
		if ev.Kind == store.BusinessEventTeamFrozen || ev.Kind == store.BusinessEventTeamUnfrozen {
			got = append(got, ev.Kind+" by "+ev.Actor+": "+ev.Attrs["reason"].(string))
		}
	}
	want := []string{
		"team.frozen by sparkwing-controller admin: console down, abuse in progress",
		"team.unfrozen by sparkwing-controller admin: abuse stopped",
		"team.unfrozen by sparkwing-controller admin: dispute won",
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = %q\nwant %q", got, want)
	}
}

func TestAdminGrantsCreditsOnceWithTheConsolesRecord(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	st := openState(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	type granted struct {
		Team         string `json:"team"`
		GrantID      string `json:"grant_id"`
		AmountMicro  int64  `json:"amount_micro"`
		Reference    string `json:"reference"`
		Created      bool   `json:"created"`
		BalanceMicro int64  `json:"balance_micro"`
	}
	grant := func(args ...string) (granted, string) {
		t.Helper()
		var out, audit bytes.Buffer
		if err := runAdmin(append([]string{"grant", "--team", "default", "--cents", "500", "--reason", "QA fixture"}, args...), &out, &audit); err != nil {
			t.Fatalf("admin grant %q: %v", args, err)
		}
		var g granted
		if err := json.Unmarshal(out.Bytes(), &g); err != nil {
			t.Fatalf("admin grant printed %q: %v", out.String(), err)
		}
		return g, audit.String()
	}
	first, audit := grant("--key", "qa-1")
	want := granted{Team: "default", GrantID: first.GrantID, AmountMicro: 500 * store.MicroCreditsPerCent, Reference: "admin:qa-1", Created: true, BalanceMicro: 500 * store.MicroCreditsPerCent}
	if first != want || first.GrantID == "" {
		t.Fatalf("first grant = %+v, want %+v", first, want)
	}
	for _, field := range []string{`"principal_kind":"break-glass"`, `"command":"sparkwing-controller admin grant"`, `"target_team":"default"`, `"amount_micro":5000000`} {
		if !strings.Contains(audit, field) {
			t.Errorf("audit line %q lacks %s", audit, field)
		}
	}
	if again, _ := grant("--key", "qa-1"); again.Created || again.GrantID != first.GrantID || again.BalanceMicro != first.BalanceMicro {
		t.Fatalf("retried grant = %+v, want the first grant unchanged", again)
	}
	if err := runAdmin([]string{"grant", "--team", "default", "--cents", "700", "--reason", "QA", "--key", "qa-1"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("a reused key with different terms granted")
	}
	if err := runAdmin([]string{"grant", "--team", "default", "--cents", "10001", "--reason", "QA", "--key", "big"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "SPARKWING_ADMIN_GRANT_ALLOW_LARGE=1") {
		t.Fatalf("grant over $100 without the flag = %v", err)
	}
	t.Setenv("SPARKWING_ADMIN_GRANT_ALLOW_LARGE", "1")
	if large, _ := grant("--key", "big", "--cents", "10001"); !large.Created || large.AmountMicro != 10001*store.MicroCreditsPerCent {
		t.Fatalf("flagged large grant = %+v", large)
	}
	t.Setenv("SPARKWING_ADMIN_GRANT_ALLOW_LARGE", "")
	if fresh, _ := grant(); !fresh.Created || !strings.HasPrefix(fresh.Reference, "admin:") || fresh.BalanceMicro != 11001*store.MicroCreditsPerCent {
		t.Fatalf("keyless grant = %+v", fresh)
	}
	st = openState(t)
	defer func() { _ = st.Close() }()
	tn, err := st.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	events, err := tn.BusinessEvents(context.Background(), store.BusinessEventCreditGranted, 1000, "")
	if err != nil || len(events) != 3 {
		t.Fatalf("grant events = %+v, %v", events, err)
	}
	for _, ev := range events {
		if ev.Actor != breakGlassActor || ev.Attrs["reason"] != "QA fixture" {
			t.Errorf("grant event = %+v", ev)
		}
	}
}

func openState(t *testing.T) *store.Store {
	t.Helper()
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := openControllerStore(context.Background(), deployment.Config{}, p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	teststore.RegisterSQLite(st, p.StateDB())
	if err := writeDeploymentRecord(t.Context(), p.StateDB(), deployment.Config{}, "", st); err != nil {
		t.Fatal(err)
	}
	return st
}

// Break-glass acts only on the store the serving controller recorded: an
// operator in the wrong pod gets a refusal rather than a fresh SQLite store,
// and a store that is not the recorded one is refused by identity.
func TestAdminActsOnlyOnTheRecordedStore(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	freeze := func() error {
		return runAdmin([]string{"freeze", "--team", "default", "--reason", "abuse"}, &bytes.Buffer{}, &bytes.Buffer{})
	}
	if err := freeze(); err == nil || !strings.Contains(err.Error(), "deployment.json") {
		t.Fatalf("admin on an empty home = %v, want a refusal naming the deployment record", err)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admin created a store: %v", err)
	}

	if err := openState(t).Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(p.StateDB() + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := freeze(); err == nil || !strings.Contains(err.Error(), "open existing store") {
		t.Fatalf("admin with a record and no store = %v, want an open-existing refusal", err)
	}
	other, err := openControllerStore(t.Context(), deployment.Config{}, p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := freeze(); err == nil || !strings.Contains(err.Error(), "recorded") {
		t.Fatalf("admin on a store other than the recorded one = %v, want an identity refusal", err)
	}

	// A record naming another connection target, as when pg-url now points
	// at a restored clone elsewhere, is refused before admin acts.
	path := deploymentRecordPath(p.StateDB())
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec deploymentRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatal(err)
	}
	rec.Connection.Target = "postgres://10.0.0.9:5432/sparkwing/public"
	if body, err = json.Marshal(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := freeze(); err == nil || !strings.Contains(err.Error(), "connected to") {
		t.Fatalf("admin on another connection target = %v, want a target refusal", err)
	}
}

func TestSQLiteControllerLockPreservesAdminAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	lock, err := wingd.AcquireControllerLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	st := openState(t)
	defer func() { _ = st.Close() }()
	if err := runAdmin([]string{"freeze", "--team=default", "--reason=abuse"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("admin while controller owns store: %v", err)
	}
}

func TestSQLiteControllerRefusesAnOwnedStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	lock, err := wingd.AcquireControllerLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := run([]string{"--addr=127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "already owns store directory") {
		t.Fatalf("second controller: %v", err)
	}
	if _, err := os.Stat(home + "/state.db"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second controller opened the store: %v", err)
	}
}
