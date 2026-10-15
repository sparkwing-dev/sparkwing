package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func storeDataServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, team := range []store.Team{"a", "b"} {
		if err := s.AsOperator().CreateTeam(t.Context(), team); err != nil {
			t.Fatal(err)
		}
	}
	return New(s, nil)
}

func storeDataRequest(t *testing.T, s *Server, method, path, body string, principal Principal) *http.Request {
	t.Helper()
	_, _, authority := newOperatorSettingsFixture(t)
	if !principal.HasScope(ScopeAdmin) {
		authority.principal.Capability.Grants = []capability.Grant{capability.RunsRead}
	}
	s.WithAuthorization(RouteAuthorization{Authenticator: capability.NewAuthenticator(authority), Bindings: authority})
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+authority.raw)
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(contextWithPrincipal(r.Context(), &principal))
}

func TestStoreDataStorageRoutesScopeQuotaAndReplicaReports(t *testing.T) {
	s := storeDataServer(t)
	admin := Principal{Name: "operator", Scopes: []string{ScopeAdmin}, Team: store.DefaultTeam}
	r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/a", `{"max_bytes_per_month":123}`, admin)
	r.SetPathValue("team", "a")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("quota=%d %s", w.Code, w.Body.String())
	}
	var quota storageQuotaJSON
	if err := json.Unmarshal(w.Body.Bytes(), &quota); err != nil || quota.Team != "a" || quota.MaxBytesPerMonth != 123 {
		t.Fatalf("quota=%+v: %v", quota, err)
	}
	r = storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/a/allowance", `{"storage_allowance_bytes":456}`, admin)
	r.SetPathValue("team", "a")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("allowance=%d %s", w.Code, w.Body.String())
	}
	for _, team := range []store.Team{"a", "b"} {
		tenant, err := s.store.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		for _, replica := range []string{"r1", "r2"} {
			amount := int64(7)
			if team == "b" {
				amount = 100
			}
			if err := tenant.AddUsage(t.Context(), store.UsageDelta{Period: store.StorageMonth(time.Now()), ReplicaID: replica, Counter: "storage_bytes", Amount: amount}); err != nil {
				t.Fatal(err)
			}
		}
	}
	r = storeDataRequest(t, s, http.MethodGet, "/api/v1/storage?team=b", "", Principal{Name: "same-name", Team: "a", Scopes: []string{ScopeRunsRead}})
	w = httptest.NewRecorder()
	s.handleStorageShow(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("read=%d %s", w.Code, w.Body.String())
	}
	var got storageStateJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Quota.Team != "a" || got.Quota.StorageAllowanceBytes != 456 || got.Usage.MonthBytes != 14 || len(got.Quotas) != 0 || len(got.Teams) != 0 || got.Database != nil {
		t.Fatalf("scoped report=%+v", got)
	}
	r = storeDataRequest(t, s, http.MethodGet, "/api/v1/operator/object-store", "", admin)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin read=%d %s", w.Code, w.Body.String())
	}
	got = storageStateJSON{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Quotas) != 1 || len(got.Teams) != 2 || got.Teams[0].Team != "b" || got.Teams[0].Bytes != 200 {
		t.Fatalf("operator report=%+v", got)
	}
}

func TestStoreDataStorageRoutesRefuseMissingTeamAndNonOperator(t *testing.T) {
	s := storeDataServer(t)
	for _, tc := range []struct {
		team   string
		scopes []string
		want   int
	}{
		{"a", []string{ScopeRunsRead}, http.StatusForbidden},
		{"missing", []string{ScopeAdmin}, http.StatusNotFound},
	} {
		r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/"+tc.team, `{"max_bytes_per_month":123}`, Principal{Team: "a", Scopes: tc.scopes})
		r.SetPathValue("team", tc.team)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("team=%s response=%d %s", tc.team, w.Code, w.Body.String())
		}
	}
	if quotas, err := s.store.AsOperator().ListStorageQuotas(t.Context()); err != nil || len(quotas) != 0 {
		t.Fatalf("refusal wrote quota=%+v: %v", quotas, err)
	}
	r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/a/allowance", `{"storage_allowance_bytes":-1}`, Principal{Team: store.DefaultTeam, Scopes: []string{ScopeAdmin}})
	r.SetPathValue("team", "a")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("negative allowance=%d %s", w.Code, w.Body.String())
	}
}

func TestStoreDataStorageSettingsRouteUsesFinalPolicy(t *testing.T) {
	s := storeDataServer(t)
	r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/settings", `{"run_retention_days":90,"node_metric_retention_days":7,"database_alarm_bytes":123,"default_tier":"paid"}`, Principal{Team: store.DefaultTeam, Scopes: []string{ScopeAdmin}})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("settings=%d %s", w.Code, w.Body.String())
	}
	got, err := s.store.AsOperator().StorageSettings(t.Context())
	if err != nil || got != (store.StorageSettings{RunRetentionDays: 90, NodeMetricRetentionDays: 7, DatabaseAlarmBytes: 123, DefaultTier: "paid"}) {
		t.Fatalf("stored policy=%+v: %v", got, err)
	}
}
