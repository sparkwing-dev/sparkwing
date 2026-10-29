package controller

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestUnmatchedRouteLogsSanitizedPathProperty(t *testing.T) {
	s := New(nil, nil)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	handler := s.HTTP().API
	property := func(input []byte, write bool) bool {
		secret := "private-" + hex.EncodeToString(input)
		method, message := "GET", "http"
		if write {
			method, message = "POST", "audit"
		}
		for _, tc := range []struct{ path, want string }{
			{"/api/v1/runs/run-1/jobs/stamp/missing", "/api/v1/runs/run-1/jobs/stamp/missing"},
			{"/api/v1/secrets/" + secret + "/missing", "/api/v1/secrets/%5Bredacted%5D/missing"},
			{"/missing/" + secret + "/%0A%0D", "/missing/%2A%2A%2A/%0A%0D"},
			{"/api/v1/tokens/" + url.PathEscape("unregistered-"+string(input)) + "/missing", "/api/v1/tokens/%5Bredacted%5D/missing"},
			{"/missing/swk_" + hex.EncodeToString(input) + "opaque", "/missing/%5Bredacted%5D"},
			{"/missing/swn_" + hex.EncodeToString(input) + "opaque", "/missing/%5Bredacted%5D"},
		} {
			logs.Reset()
			w := httptest.NewRecorder()
			ctx, masker := withRequestMasker(context.Background())
			masker.Register(secret)
			r := httptest.NewRequest(method, tc.path+"?token="+secret, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+secret)
			handler.ServeHTTP(w, r)
			records := auditLines(t, &logs, message)
			if w.Code != 404 || len(records) != 1 || records[0]["path"] != tc.want || records[0]["method"] != method || records[0]["route"] != "other" || strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "?token") {
				t.Log(records, w.Code)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditPolicyNeverLogsSecretPathsOrCredentialsProperty(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.facts.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TeamAdmin}
	var logs bytes.Buffer
	f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	property := func(input []byte) bool {
		logs.Reset()
		secret := "secret-" + hex.EncodeToString(input)
		r := httptest.NewRequest(http.MethodDelete, "/api/v1/secrets/"+url.PathEscape(secret)+"?reason=private-query", nil)
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
		for key, want := range map[string]any{"request_id": w.Header().Get(RequestIDHeader), "method": "DELETE", "route": "/api/v1/secrets/{name}", "principal_id": "pusher", "principal_kind": "account", "team": string(store.DefaultTeam), "client_class": "other"} {
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
		return w.Header().Get(RequestIDHeader) != "audit-1" && len(w.Header().Get(RequestIDHeader)) == 24
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
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
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
	f.s.WithEngineController(&EngineController{ResolveTenant: func(context.Context, store.Team) (TenantHandle, error) {
		return selectorTransactions{RunTransactions: f.writer.Tenant, team: store.DefaultTeam}, nil
	}})
	var logs bytes.Buffer
	f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-7/jobs/build/cancel", strings.NewReader(`{"reason":"cancel"}`))
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
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	r.Header.Set(RequestIDHeader, "bad id")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	id := w.Header().Get(RequestIDHeader)
	if _, err := hex.DecodeString(id); err != nil || len(id) != 24 {
		t.Fatalf("request id=%q", id)
	}
}
