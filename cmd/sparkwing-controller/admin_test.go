package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	if _, err := st.DB().ExecContext(ctx, "INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at) VALUES (?,?,?,?,?)", "dp_1", store.DefaultTeam, "", "chargeback", time.Now().UnixNano()); err != nil {
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
	events, err := tn.BusinessEvents(ctx, "")
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

func openState(t *testing.T) *store.Store {
	t.Helper()
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := openControllerStore(context.Background(), p.StateDB(), "")
	if err != nil {
		t.Fatal(err)
	}
	return st
}
