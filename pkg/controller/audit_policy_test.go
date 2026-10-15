package controller

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAuditPolicyNeverLogsSecretPathsOrCredentialsProperty(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TeamAdmin}
	var logs bytes.Buffer
	f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	property := func(input []byte) bool {
		logs.Reset()
		secret := "secret-" + hex.EncodeToString(input)
		r := httptest.NewRequest("DELETE", "/api/v1/secrets/"+url.PathEscape(secret)+"?reason=private-query", nil)
		r.Header.Set("Authorization", "Bearer "+f.raw)
		r.Header.Set(RequestIDHeader, "audit-1")
		r.Header.Set("User-Agent", "pusher@example.test leaked-user-agent")
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, r)
		records := auditLines(t, &logs, "audit")
		if len(records) != 1 {
			t.Log(logs.String())
			return false
		}
		rec := records[0]
		for key, want := range map[string]any{"request_id": "audit-1", "method": "DELETE", "route": "/api/v1/secrets/{name}", "principal_id": "pusher", "principal_kind": "account", "team": string(store.DefaultTeam), "client_class": "other"} {
			if rec[key] != want {
				t.Log(rec)
				return false
			}
		}
		for _, leak := range []string{secret, f.raw, "private-query", "pusher@example.test", "leaked-user-agent"} {
			if strings.Contains(logs.String(), leak) {
				return false
			}
		}
		return w.Header().Get(RequestIDHeader) == "audit-1"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditPolicyRecordsSensitiveReadsAndDeniedSubjects(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	var logs bytes.Buffer
	f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	for _, tc := range []struct {
		path    string
		grants  []capability.Grant
		raw     string
		subject bool
	}{
		{"/api/v1/secrets/hidden", []capability.Grant{capability.TeamAdmin}, f.raw, true},
		{"/api/v1/team/invitations", nil, f.raw, true},
		{"/api/v1/runs", nil, "swk_invalid", false},
	} {
		logs.Reset()
		f.facts.snapshot.Authorities[0].Principal.Capability.Grants = tc.grants
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.raw)
		f.s.Handler().ServeHTTP(httptest.NewRecorder(), r)
		records := auditLines(t, &logs, "audit")
		if len(records) != 1 || (records[0]["principal_id"] == "pusher") != tc.subject {
			t.Fatalf("audit=%v", records)
		}
	}
}

func TestAuditPolicyRecordsFinalJobPathIDs(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.s.WithEngineController(&EngineController{ResolveRunTeam: func(context.Context, string) (store.Team, error) { return store.DefaultTeam, nil }})
	var logs bytes.Buffer
	f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	r := httptest.NewRequest("POST", "/api/v1/runs/run-7/jobs/build/cancel", strings.NewReader(`{"reason":"cancel"}`))
	r.Header.Set("Authorization", "Bearer "+f.raw)
	r.Header.Set("Content-Type", "application/json")
	f.s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	records := auditLines(t, &logs, "audit")
	if len(records) != 1 || records[0]["run_id"] != "run-7" || records[0]["job_id"] != "build" {
		t.Fatalf("audit=%v", records)
	}
}

func TestAuditPolicySkipsFinalEmptyPolls(t *testing.T) {
	s := newAdviceServer(t)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	w := budgetRequest(s, "runner")
	if w.Code != 200 || len(auditLines(t, &logs, "audit")) != 0 {
		t.Fatalf("empty poll=%d logs=%s", w.Code, logs.String())
	}
	r := httptest.NewRequest("GET", "/api/v1/health", nil)
	r.Header.Set(RequestIDHeader, "bad id")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Header().Get(RequestIDHeader) == "" || strings.Contains(w.Header().Get(RequestIDHeader), "bad") {
		t.Fatalf("request id=%q", w.Header().Get(RequestIDHeader))
	}
}
