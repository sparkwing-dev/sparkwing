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
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func storeDataServer(t *testing.T) *serverFixture {
	t.Helper()
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, team := range []store.Team{"a", "b"} {
		teststore.SeedTeam(t, s, team)
	}
	return &serverFixture{Server: New(s, nil).WithOperatorSettingsWriter(storeSettingsWriterFake{store: s}), store: s}
}

func storeDataRequest(t *testing.T, s *serverFixture, method, path, body string, operator bool) *http.Request {
	t.Helper()
	_, _, authority := newOperatorSettingsFixture(t)
	if !operator {
		authority.principal.Capability.Grants = []capability.Grant{capability.RunsRead}
	}
	s.WithAuthorization(RouteAuthorization{Authenticator: capability.NewAuthenticator(authority), Bindings: authority})
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+authority.raw)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestStoreDataStorageRoutesScopeQuotaAndReplicaReports(t *testing.T) {
	s := storeDataServer(t)
	admin := true
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
	r = storeDataRequest(t, s, http.MethodGet, "/api/v1/storage?team=b", "", false)
	w = httptest.NewRecorder()
	if err := s.handleStorageShow(w, r, capability.Authorized{Target: capability.Target{Team: "a"}}); err != nil {
		t.Fatal(err)
	}
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
		team     string
		operator bool
		want     int
	}{
		{"a", false, http.StatusForbidden},
		{"missing", true, http.StatusNotFound},
	} {
		r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/"+tc.team, `{"max_bytes_per_month":123}`, tc.operator)
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
	r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/quotas/a/allowance", `{"storage_allowance_bytes":-1}`, true)
	r.SetPathValue("team", "a")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("negative allowance=%d %s", w.Code, w.Body.String())
	}
}

func TestStoreDataStorageSettingsRouteUsesFinalPolicy(t *testing.T) {
	s := storeDataServer(t)
	r := storeDataRequest(t, s, http.MethodPut, "/api/v1/operator/object-store/settings", `{"run_retention_days":90,"node_metric_retention_days":7,"database_alarm_bytes":123,"default_tier":"paid"}`, true)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("settings=%d %s", w.Code, w.Body.String())
	}
	got, err := s.store.AsOperator().StorageSettings(t.Context())
	if err != nil || got != (store.StorageSettings{AuditRetentionDays: 400, RunRetentionDays: 90, NodeMetricRetentionDays: 7, DatabaseAlarmBytes: 123, DefaultTier: "paid"}) {
		t.Fatalf("stored policy=%+v: %v", got, err)
	}
}
