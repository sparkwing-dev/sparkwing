package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// A node credential naming another team's run, a run's child it did not start, or
// a claim that is not its own is audited, and each refusal answers the same for an
// existing target as for a missing one.
func TestAccessRefusalAuditsNodeCredentialProbes(t *testing.T) {
	e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	node := award.Claim.Capability
	engineAdmitBearer(t, e.Store, node)
	teststore.SeedTeam(t, e.Store, "b")
	now := time.Now()
	if err := teststore.SeedRun(e.Store, t.Context(), "b", store.Run{RunSummary: runstate.RunSummary{ID: "run-b", Pipeline: "build-b", Status: "running", CreatedAt: now, StartedAt: now}}); err != nil {
		t.Fatal(err)
	}
	transition := `{"to":"running","cause":"start","source_attempt":0}`
	for _, tc := range []struct {
		method, existing, missing, body, route, refusal string
	}{
		{"POST", "/api/v1/runs/run-b/jobs/job/transition", "/api/v1/runs/ghost/jobs/job/transition", transition, "/api/v1/runs/{run}/jobs/{job}/transition", "not_found"},
		{"GET", "/api/v1/runs/run/children/run-b", "/api/v1/runs/run/children/ghost", "", "/api/v1/runs/{run}/children/{child}", "binding"},
		// safety: a claim route compares the path with the credential's own claim and looks nothing up.
		{"POST", "/api/v1/claims/run-b/heartbeat", "/api/v1/claims/ghost/heartbeat", "{}", "/api/v1/claims/{claim}/heartbeat", "binding"},
	} {
		existing := refusalProbe(h, node, tc.method, tc.existing, tc.body)
		missing := refusalProbe(h, node, tc.method, tc.missing, tc.body)
		if existing.status < 400 || existing.status != missing.status || existing.body != missing.body {
			t.Errorf("%s: existing %d %s, missing %d %s", tc.route, existing.status, existing.body, missing.status, missing.body)
		}
		var rows []teststore.AccessRefusalRow
		for _, row := range teststore.AccessRefusals(t, e.Store) {
			if row.Attrs["route"] == tc.route {
				rows = append(rows, row)
			}
		}
		if len(rows) != 1 || rows[0].Attrs["refusal"] != tc.refusal || rows[0].Attrs["credential"] != "node" || rows[0].Attrs["presentation"] != "bearer" || rows[0].Team != "a" {
			t.Errorf("%s rows = %+v", tc.route, rows)
		}
	}
}

type probeResult struct {
	status int
	body   string
}

func refusalProbe(h http.Handler, bearer, method, path, body string) probeResult {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return probeResult{w.Code, w.Body.String()}
}

type refusalAuditFake struct {
	now       time.Time
	fail      bool
	writes    []store.AccessRefusal
	summaries []store.AccessSuppression
	log       bytes.Buffer
}

func (f *refusalAuditFake) audit() *refusalAudit {
	return &refusalAudit{
		now:    func() time.Time { return f.now },
		logger: slog.New(slog.NewTextHandler(&f.log, nil)),
		record: func(_ context.Context, r store.AccessRefusal) error {
			if f.fail {
				return errors.New("store unavailable")
			}
			f.writes = append(f.writes, r)
			return nil
		},
		suppress: func(_ context.Context, s store.AccessSuppression) error {
			f.summaries = append(f.summaries, s)
			return nil
		},
	}
}

func refusalFrom(team, subject, pattern string) routepolicy.Refusal {
	p := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{Team: team, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: subject}}}
	return routepolicy.Refusal{Principal: p, Method: "POST", Pattern: pattern, Class: routepolicy.RefusalNotFound}
}

func TestRefusalAuditWritesOncePerKeyAndWindow(t *testing.T) {
	f := &refusalAuditFake{now: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	audit := f.audit()
	probe := refusalFrom("b", "runner-1", "/api/v1/runs/{run}/cancel")
	for range 100 {
		audit.note(t.Context(), probe)
	}
	if len(f.writes) != 1 {
		t.Fatalf("100 probes in one window made %d writes", len(f.writes))
	}
	audit.note(t.Context(), refusalFrom("b", "runner-1", "/api/v1/runs/{run}"))
	f.now = f.now.Add(store.AccessRefusalWindow)
	audit.note(t.Context(), probe)
	if len(f.writes) != 3 {
		t.Fatalf("a new route and a new window made %d writes in total, want 3", len(f.writes))
	}
	audit.note(t.Context(), refusalFrom("", "operator", "/api/v1/runs/{run}/cancel"))
	if len(f.writes) != 3 {
		t.Fatal("a principal without a team wrote a row no team view can read")
	}
}

// One team's many credentials stop at the per-team cap, the overflow is counted in
// one summary when the window rolls, and other teams keep their own budget.
func TestRefusalAuditCapsEachTeamAndSummarizesTheRest(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	f := &refusalAuditFake{now: start}
	audit := f.audit()
	const runners = refusalTeamKeys + 120
	for i := range runners {
		for range 3 {
			audit.note(t.Context(), refusalFrom("b", fmt.Sprintf("runner-%d", i), "/api/v1/runs/{run}/claim"))
		}
	}
	if len(f.writes) != refusalTeamKeys {
		t.Fatalf("team b wrote %d rows, want its cap %d", len(f.writes), refusalTeamKeys)
	}
	audit.note(t.Context(), refusalFrom("c", "runner-c", "/api/v1/runs/{run}/claim"))
	if len(f.writes) != refusalTeamKeys+1 || f.writes[len(f.writes)-1].Principal.Capability.Team != "c" {
		t.Fatal("team b's cap cost team c its row")
	}
	if len(f.summaries) != 0 {
		t.Fatalf("summary written before the window ended: %+v", f.summaries)
	}
	f.now = start.Add(store.AccessRefusalWindow)
	audit.note(t.Context(), refusalFrom("c", "runner-c", "/api/v1/runs/{run}/claim"))
	audit.note(t.Context(), refusalFrom("c", "runner-c", "/api/v1/runs/{run}"))
	want := store.AccessSuppression{At: f.now, Team: "b", Window: start, Suppressed: 120 * 3}
	if len(f.summaries) != 1 || f.summaries[0] != want {
		t.Fatalf("summaries = %+v, want only %+v", f.summaries, want)
	}
	audit.note(t.Context(), refusalFrom("b", "runner-0", "/api/v1/runs/{run}/claim"))
	if got := f.writes[len(f.writes)-1]; got.Principal.Capability.Team != "b" || len(audit.teams["b"].keys) != 1 {
		t.Fatal("the new window did not reset team b's keys")
	}
}

// A store that keeps refusing writes warns once a minute with a failure count, not once per probe.
func TestRefusalAuditRateLimitsFailureWarnings(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	f := &refusalAuditFake{now: start, fail: true}
	audit := f.audit()
	for i := range 50 {
		audit.note(t.Context(), refusalFrom("b", "runner-1", "/api/v1/runs/{run}/cancel"))
		f.now = f.now.Add(time.Second)
		if i == 0 && strings.Count(f.log.String(), "access refusal audit failed") != 1 {
			t.Fatalf("first failure did not warn: %s", f.log.String())
		}
	}
	if n := strings.Count(f.log.String(), "access refusal audit failed"); n != 1 {
		t.Fatalf("50 failures in 50s warned %d times", n)
	}
	f.now = start.Add(refusalWarnEvery)
	audit.note(t.Context(), refusalFrom("b", "runner-1", "/api/v1/runs/{run}/cancel"))
	if n := strings.Count(f.log.String(), "access refusal audit failed"); n != 2 || !strings.Contains(f.log.String(), "failures=50") {
		t.Fatalf("after a minute: %d warnings: %s", n, f.log.String())
	}
	f.fail = false
	audit.note(t.Context(), refusalFrom("b", "runner-1", "/api/v1/runs/{run}/cancel"))
	if len(f.writes) != 1 {
		t.Fatalf("a failed key was not retried once the store recovered: %d writes", len(f.writes))
	}
}
