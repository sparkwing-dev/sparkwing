package store_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func accessRefusalDialects(t *testing.T, run func(t *testing.T, st *store.Store)) {
	t.Run("sqlite", func(t *testing.T) { run(t, storetest.OpenSQLite(t)) })
	t.Run("postgres", func(t *testing.T) { run(t, storetest.OpenPostgres(t)) })
}

func TestAccessRefusalKeepsOneRowPerActorRouteAndWindow(t *testing.T) {
	accessRefusalDialects(t, func(t *testing.T, st *store.Store) {
		teststore.SeedTeam(t, st, "b")
		op := st.AsOperator()
		account := capability.Principal{
			Class:      capability.CredentialSession,
			Authority:  capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session-digest"},
			Capability: capability.Capability{Team: "b", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "acct-b"}},
		}
		runner := capability.Principal{
			Class:      capability.CredentialBearer,
			Authority:  capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "swk_runnerprefix"},
			Capability: capability.Capability{Team: "b", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner-b"}},
		}
		node := runner
		node.Capability.Unit, node.Capability.Binding = capability.UnitNode, capability.Binding{Kind: capability.BindingNode, RunID: "run-b", NodeID: "job", Generation: 1, Holder: "host-b"}
		start := time.Date(2026, 3, 1, 10, 5, 0, 0, time.UTC)
		record := func(at time.Time, p capability.Principal, method, route, refusal string) {
			t.Helper()
			if err := op.RecordAccessRefusal(t.Context(), store.AccessRefusal{At: at, Principal: p, Method: method, Route: route, Refusal: refusal}); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 25 {
			record(start.Add(time.Duration(i)*time.Minute), account, "POST", "/api/v1/runs/{run}/cancel", "not_found")
		}
		if rows := teststore.AccessRefusals(t, st); len(rows) != 1 {
			t.Fatalf("a probe loop within one window wrote %d rows, want 1", len(rows))
		}
		record(start.Add(store.AccessRefusalWindow), account, "POST", "/api/v1/runs/{run}/cancel", "not_found")
		record(start, account, "GET", "/api/v1/runs/{run}", "not_found")
		record(start, runner, "POST", "/api/v1/runs/{run}/claim", "not_found")
		record(start, node, "POST", "/api/v1/claims/{claim}/heartbeat", "binding")
		rows := teststore.AccessRefusals(t, st)
		if len(rows) != 5 {
			t.Fatalf("rows = %d, want one per window, route and credential: %+v", len(rows), rows)
		}
		credentials := map[string]bool{}
		for _, row := range rows {
			credentials[row.Attrs["credential"].(string)] = true
			if row.Team != "b" || row.RunID != "" || row.Account != "" || len(row.Subject) != 64 {
				t.Errorf("row carries more than the refusal: %+v", row)
			}
			if keys := slices.Sorted(maps.Keys(row.Attrs)); !slices.Equal(keys, []string{"credential", "method", "presentation", "refusal", "route"}) {
				t.Errorf("attrs keys = %v", keys)
			}
		}
		if !credentials["account"] || !credentials["runner"] || !credentials["node"] {
			t.Errorf("credential kinds = %v", credentials)
		}
		for _, row := range rows {
			if row.Attrs["credential"] == "account" && row.Actor != "acct-b" {
				t.Errorf("account actor = %q", row.Actor)
			}
			if row.Attrs["credential"] != "account" && (!strings.HasPrefix(row.Actor, "machine:") || strings.Contains(row.Actor, "runner-b")) {
				t.Errorf("machine actor must be a digest: %q", row.Actor)
			}
		}
	})
}

func TestAccessRefusalAcceptsOnlyRegisteredRoutesAndClosedClasses(t *testing.T) {
	st := storetest.OpenSQLite(t)
	p := capability.Principal{
		Class:      capability.CredentialBearer,
		Authority:  capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "swk_prefix"},
		Capability: capability.Capability{Team: "b", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "acct-b"}},
	}
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []store.AccessRefusal{
		{At: at, Principal: p, Method: "POST", Route: "/api/v1/runs/run-a/cancel", Refusal: "not_found"},
		{At: at, Principal: p, Method: "GET", Route: "/api/v1/runs/{run}/cancel", Refusal: "not_found"},
		{At: at, Principal: p, Method: "POST", Route: "/api/v1/runs/{run}/cancel", Refusal: "exists_elsewhere"},
		{Principal: p, Method: "POST", Route: "/api/v1/runs/{run}/cancel", Refusal: "not_found"},
	} {
		if err := st.AsOperator().RecordAccessRefusal(t.Context(), tc); !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("%+v: err = %v, want invalid input", tc, err)
		}
	}
	if rows := teststore.AccessRefusals(t, st); len(rows) != 0 {
		t.Fatalf("invalid refusals wrote rows: %+v", rows)
	}
}
