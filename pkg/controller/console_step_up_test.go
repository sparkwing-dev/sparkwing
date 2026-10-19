package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestConsoleWritesRequireARecentSignIn(t *testing.T) {
	s, h, raw, csrf := creditPolicyFixture(t)
	creditPolicyWrite(t, h, raw, csrf, `{"warm_cpu_class_cores":1}`, http.StatusOK)
	engineSQL(t, s.store, `UPDATE sessions SET created_at=?`, time.Now().Add(-11*time.Minute).UnixNano())
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/operator/tokens", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("console read after the step-up window: %d %s", w.Code, w.Body.String())
	}
	before, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	creditPolicyWrite(t, h, raw, csrf, `{"warm_cpu_class_cores":3}`, http.StatusForbidden)
	after, err := s.store.AsOperator().CreditSettings(t.Context())
	if err != nil || after.WarmCPUClassCores != before.WarmCPUClassCores {
		t.Fatalf("stale console write committed: %+v %v", after, err)
	}
}

func TestConsoleWriteRefusalNamesReauthentication(t *testing.T) {
	s, _, raw, _ := creditPolicyFixture(t)
	engineSQL(t, s.store, `UPDATE sessions SET created_at=?`, time.Now().Add(-11*time.Minute).UnixNano())
	route, _ := capability.LookupRoute("PUT", "/api/v1/operator/credits/settings")
	_, err := s.authorization.Authenticator.Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || fmt.Sprint(failure.Details["reason"]) != string(apierr.ReasonReauthRequired) {
		t.Fatalf("stale console write: %v", err)
	}
}

func TestConsolePrincipalCarriesNoSessionTeam(t *testing.T) {
	s, _, raw, _ := creditPolicyFixture(t)
	for _, path := range []string{"/api/v1/operator/tokens", "/api/v1/operator/limits"} {
		route, _ := capability.LookupRoute("GET", path)
		authenticated, err := s.authorization.Authenticator.Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
		if err != nil {
			t.Fatal(err)
		}
		if p := authenticated.Principal; p.Class != capability.CredentialConsole || p.Capability.Team != "" {
			t.Fatalf("%s console principal = %s team %q", path, p.Class, p.Capability.Team)
		}
	}
}
