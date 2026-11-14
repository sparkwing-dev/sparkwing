package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestRefusalAuditWritesOncePerKeyAndWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	var writes []store.AccessRefusal
	fail := false
	audit := &refusalAudit{
		now:    func() time.Time { return now },
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		record: func(_ context.Context, r store.AccessRefusal) error {
			if fail {
				return errors.New("store unavailable")
			}
			writes = append(writes, r)
			return nil
		},
	}
	p := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{Team: "b", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "acct-b"}}}
	probe := routepolicy.Refusal{Principal: p, Method: "POST", Pattern: "/api/v1/runs/{run}/cancel", Class: routepolicy.RefusalNotFound}
	for range 100 {
		audit.note(t.Context(), probe)
	}
	if len(writes) != 1 {
		t.Fatalf("100 probes in one window made %d writes", len(writes))
	}
	other := probe
	other.Pattern = "/api/v1/runs/{run}"
	audit.note(t.Context(), other)
	now = now.Add(store.AccessRefusalWindow)
	audit.note(t.Context(), probe)
	if len(writes) != 3 {
		t.Fatalf("a new route and a new window made %d writes in total, want 3", len(writes))
	}
	fail = true
	retry := probe
	retry.Class = routepolicy.RefusalBinding
	audit.note(t.Context(), retry)
	fail = false
	audit.note(t.Context(), retry)
	if len(writes) != 4 {
		t.Fatalf("a failed write was not retried: %d writes", len(writes))
	}
	for i := range maxRefusalKeys + 10 {
		next := probe
		next.Principal.Capability.Subject.ID = "acct-" + string(rune('a'+i%26)) + time.Duration(i).String()
		audit.note(t.Context(), next)
	}
	if len(audit.seen) > maxRefusalKeys || len(writes) != 4+maxRefusalKeys+10 {
		t.Fatalf("seen=%d writes=%d", len(audit.seen), len(writes))
	}
}
