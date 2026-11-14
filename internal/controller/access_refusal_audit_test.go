package controller_test

import (
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func refusalsFor(rows []teststore.AccessRefusalRow, route string) []teststore.AccessRefusalRow {
	var out []teststore.AccessRefusalRow
	for _, row := range rows {
		if row.Attrs["route"] == route {
			out = append(out, row)
		}
	}
	return out
}

// A refused cross-team write leaves one audit row in the prober's own team,
// naming the route template and closed classes and nothing it was refused.
func TestAccessRefusalAuditsOneRowPerCrossTeamProbe(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		if code, body := f.do("POST", "/api/v1/runs/"+f.runA+"/cancel", f.ownerB, map[string]string{"reason": "body-marker-31c7"}); code != http.StatusNotFound {
			t.Fatalf("cross-team cancel = %d: %s", code, body)
		}
		rows := teststore.AccessRefusals(t, f.st)
		if len(rows) != 1 {
			t.Fatalf("one refused write left %d rows: %+v", len(rows), rows)
		}
		row := rows[0]
		want := map[string]any{"method": "POST", "route": "/api/v1/runs/{run}/cancel", "refusal": "not_found", "credential": "account", "presentation": "session"}
		if !maps.Equal(row.Attrs, want) || row.Team != string(f.teamB.Team()) || row.RunID != "" || row.Account != "" {
			t.Fatalf("row = %+v, want attrs %v in team %s", row, want, f.teamB.Team())
		}
		for _, private := range []string{f.runA, f.victim, string(f.teamA.Team()), "body-marker-31c7", strings.TrimPrefix(f.ownerB, "Session "), "cancel?"} {
			if strings.Contains(row.Raw, private) {
				t.Errorf("audit row holds %q: %s", private, row.Raw)
			}
		}
	})
}

// A probe loop over foreign and missing runs on one route leaves one row: the
// row's key includes its class, so a foreign run that classed differently from
// a missing one would add a second row.
func TestAccessRefusalProbeLoopIsBoundedAndCannotTellForeignFromMissing(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		for i := range 30 {
			target := []string{f.runA, "absent-run", f.victim}[i%3]
			if code, body := f.do("POST", "/api/v1/runs/"+target+"/cancel", f.ownerB, nil); code != http.StatusNotFound {
				t.Fatalf("cancel %s = %d: %s", target, code, body)
			}
			if code, body := f.do("GET", "/api/v1/runs/"+target, f.ownerB, nil); code != http.StatusNotFound {
				t.Fatalf("get %s = %d: %s", target, code, body)
			}
		}
		rows := teststore.AccessRefusals(t, f.st)
		if len(rows) != 2 {
			t.Fatalf("60 probes over two routes left %d rows, want 2: %+v", len(rows), rows)
		}
		for _, route := range []string{"/api/v1/runs/{run}/cancel", "/api/v1/runs/{run}"} {
			if got := refusalsFor(rows, route); len(got) != 1 || got[0].Attrs["refusal"] != "not_found" {
				t.Errorf("%s rows = %+v", route, got)
			}
		}
		if code, body := f.do("POST", "/api/v1/runs/"+f.runA+"/cancel", f.everyScopeB, nil); code != http.StatusNotFound {
			t.Fatalf("bearer cancel = %d: %s", code, body)
		}
		if code, body := f.do("POST", "/api/v1/runs/"+f.runA+"/claim", f.runnerB, map[string]any{}); code != http.StatusNotFound {
			t.Fatalf("runner claim = %d: %s", code, body)
		}
		rows = teststore.AccessRefusals(t, f.st)
		if len(rows) != 4 {
			t.Fatalf("another credential and route left %d rows in total, want 4: %+v", len(rows), rows)
		}
		credentials := map[string]bool{}
		for _, row := range rows {
			credentials[row.Attrs["credential"].(string)+"/"+row.Attrs["presentation"].(string)] = true
		}
		for _, want := range []string{"account/session", "account/bearer", "runner/bearer"} {
			if !credentials[want] {
				t.Errorf("no %s row in %v", want, credentials)
			}
		}
	})
}

var cspNonce = regexp.MustCompile(`nonce-[A-Za-z0-9+/=_-]+`)

// The answer to a foreign run is the answer to a missing one, byte for byte,
// and a typo in a team's own run is audited by the same rule.
func TestAccessRefusalLeavesForeignResponseIdenticalToMissing(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		for _, tc := range []struct {
			method, suffix, auth string
			body                 any
		}{
			{"GET", "", f.ownerB, nil},
			{"POST", "/cancel", f.ownerB, nil},
			{"POST", "/oidc-token", f.ownerB, map[string]string{"audience": "example.test"}},
			{"GET", "/jobs/n1", f.everyScopeB, nil},
			{"POST", "/claim", f.runnerB, map[string]any{}},
		} {
			foreign := f.serve(tc.method, "/api/v1/runs/"+f.runA+tc.suffix, tc.auth, tc.body)
			missing := f.serve(tc.method, "/api/v1/runs/absent-run"+tc.suffix, tc.auth, tc.body)
			if foreign.Code != http.StatusNotFound || foreign.Code != missing.Code || foreign.Body.String() != missing.Body.String() {
				t.Errorf("%s %s: foreign %d %q, missing %d %q", tc.method, tc.suffix, foreign.Code, foreign.Body, missing.Code, missing.Body)
			}
			for _, h := range []http.Header{foreign.Header(), missing.Header()} {
				h.Del("X-Request-Id")
				h.Del("Date")
				for key, values := range h {
					for i := range values {
						values[i] = cspNonce.ReplaceAllString(values[i], "nonce-N")
					}
					h[key] = values
				}
			}
			if !maps.EqualFunc(foreign.Header(), missing.Header(), slices.Equal) {
				t.Errorf("%s %s headers differ:\nforeign %v\nmissing %v", tc.method, tc.suffix, foreign.Header(), missing.Header())
			}
		}
		if code, body := f.do("GET", "/api/v1/runs/run-team-a-typo", f.ownerA, nil); code != http.StatusNotFound {
			t.Fatalf("own-team typo = %d: %s", code, body)
		}
		var typo []teststore.AccessRefusalRow
		for _, row := range teststore.AccessRefusals(t, f.st) {
			if row.Team == string(f.teamA.Team()) {
				typo = append(typo, row)
			}
		}
		if len(typo) != 1 || typo[0].Attrs["route"] != "/api/v1/runs/{run}" || typo[0].Attrs["refusal"] != "not_found" {
			t.Fatalf("own-team typo rows = %+v", typo)
		}
	})
}

// Refusal rows reach operators only: a team's own credentials have no read of its audit stream.
func TestAccessRefusalRowsAreNotReadableByTheProbingTeam(t *testing.T) {
	f := newTenancyFixture(t, openSQLiteStore)
	f.do("GET", "/api/v1/runs/"+f.runA, f.ownerB, nil)
	for _, auth := range []string{f.ownerB, f.everyScopeB} {
		code, body := f.do("GET", "/api/v1/operator/teams/"+string(f.teamB.Team()), auth, nil)
		if code == http.StatusOK || strings.Contains(body, "access.refused") {
			t.Errorf("team B read its audit stream: %d %s", code, body)
		}
	}
}
