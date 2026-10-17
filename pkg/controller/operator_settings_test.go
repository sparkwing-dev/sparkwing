package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type operatorSettingsFixture struct {
	limits   store.ComputeLimits
	gate     store.SignUpGate
	settings store.StorageSettings
	quota    store.StorageQuota
	writes   int
	failure  error
	seen     capability.Authorized
}

func (f *operatorSettingsFixture) ComputeLimits(context.Context) (store.ComputeLimits, error) {
	return f.limits, f.failure
}

func (f *operatorSettingsFixture) ComputeUsage(context.Context) (store.ComputeUsage, error) {
	return store.ComputeUsage{Runners: 3, ByPrincipal: map[string]int64{"other-team": 3}}, f.failure
}

func (f *operatorSettingsFixture) WriteComputeLimits(_ context.Context, a capability.Authorized, updates map[string]int64) (store.ComputeLimits, error) {
	f.seen = a
	if f.failure != nil {
		return store.ComputeLimits{}, f.failure
	}
	if len(updates) == 0 {
		return store.ComputeLimits{}, store.ErrInvalidComputeLimitSetting
	}
	for name, value := range updates {
		if !store.ValidComputeLimit(name) || value < 0 {
			return store.ComputeLimits{}, store.ErrInvalidComputeLimitSetting
		}
	}
	f.writes++
	f.limits.GlobalRunners = updates[store.ComputeLimitGlobalRunners]
	return f.limits, nil
}

func (f *operatorSettingsFixture) SignUpGate(context.Context) (store.SignUpGate, error) {
	return f.gate, f.failure
}

func (f *operatorSettingsFixture) SignUpCounts(context.Context, time.Time) (store.SignUpCounts, error) {
	return store.SignUpCounts{LastHour: 2, LastDay: 3, Waitlisted: 1}, f.failure
}

func (f *operatorSettingsFixture) WriteSignUpSettings(_ context.Context, a capability.Authorized, req store.SignUpSettingsChange) error {
	f.seen = a
	if f.failure != nil {
		return f.failure
	}
	for _, field := range []struct {
		in  *int
		out *int
	}{{req.HourlyLimit, &f.gate.Limits.HourlyLimit}, {req.DailyLimit, &f.gate.Limits.DailyLimit}, {req.HourlyWarn, &f.gate.Limits.HourlyWarn}, {req.GitHubMinAccountDays, &f.gate.Limits.GitHubMinAccountDays}, {req.FreeTeamMembers, &f.gate.Limits.FreeTeamMembers}} {
		if field.in != nil {
			*field.out = *field.in
		}
	}
	if req.Mode != nil {
		f.gate.Mode = store.SignUpMode(*req.Mode)
		f.gate.Reason = req.Reason
		f.gate.SetBy = a.Principal.Capability.Subject.ID
		f.gate.UpdatedAt = time.Now()
	}
	f.writes++
	return nil
}

func (f *operatorSettingsFixture) StorageSettings(context.Context) (store.StorageSettings, error) {
	return f.settings, f.failure
}

func (f *operatorSettingsFixture) WriteStorageSettings(_ context.Context, a capability.Authorized, settings store.StorageSettings) error {
	f.seen = a
	if f.failure != nil {
		return f.failure
	}
	f.writes++
	f.settings = settings
	return nil
}

func (f *operatorSettingsFixture) ListStorageQuotas(context.Context) ([]store.StorageQuota, error) {
	return []store.StorageQuota{f.quota}, f.failure
}

func (f *operatorSettingsFixture) TopStorageTeams(context.Context, string, int) ([]store.StorageTeamUsage, error) {
	return []store.StorageTeamUsage{{Team: "customer", Bytes: 100, Objects: 2}}, f.failure
}

func (f *operatorSettingsFixture) WriteStorageQuota(_ context.Context, a capability.Authorized, quota store.StorageQuota) (store.StorageQuota, error) {
	f.seen = a
	if f.failure != nil {
		return store.StorageQuota{}, f.failure
	}
	f.writes++
	quota.Team, quota.AllowanceBytes = store.Team(a.Target.Team), f.quota.AllowanceBytes
	f.quota = quota
	return quota, nil
}

func (f *operatorSettingsFixture) WriteStorageAllowance(_ context.Context, a capability.Authorized, size int64) (store.StorageQuota, error) {
	f.seen = a
	if f.failure != nil {
		return store.StorageQuota{}, f.failure
	}
	f.writes++
	f.quota.AllowanceBytes = size
	return f.quota, nil
}

type operatorAuthorityFixture struct {
	raw       string
	principal capability.Principal
}

func (f *operatorAuthorityFixture) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if lookup.Digest != sha256.Sum256([]byte(f.raw)) {
		return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Unauthenticated}
	}
	return capability.AuthenticationSnapshot{Now: time.Now(), Authorities: []capability.AuthorityState{{Principal: f.principal, MembershipValid: true}}}, nil
}

func (*operatorAuthorityFixture) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

func newOperatorSettingsFixture(t *testing.T) (*Server, *operatorSettingsFixture, *operatorAuthorityFixture) {
	t.Helper()
	secret := sha256.Sum256([]byte("operator settings fixture"))
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(secret[:])
	a := &operatorAuthorityFixture{raw: raw, principal: capability.Principal{
		Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "ops"},
		Capability: capability.Capability{
			Prefix: "ops", Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller,
			Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator-account"}, Binding: capability.Binding{Kind: capability.BindingNone},
			Grants: []capability.Grant{capability.OperatorLimits, capability.OperatorObjectStore},
		},
	}}
	f := &operatorSettingsFixture{gate: store.SignUpGate{Mode: store.SignUpOpen}, quota: store.StorageQuota{Team: "customer", AllowanceBytes: 32}}
	s := New(nil, nil).WithAuthorization(RouteAuthorization{Authenticator: capability.NewAuthenticator(a), Bindings: a})
	s.operatorSettings = f
	s.WithOperatorSettingsWriter(f)
	return s, f, a
}

func operatorRequest(t *testing.T, h http.Handler, raw, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+raw)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("operator response is cacheable")
	}
	return w
}

func TestOperatorSettingsHTTPRoundTrip(t *testing.T) {
	s, f, a := newOperatorSettingsFixture(t)
	h := s.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return operatorRequest(t, h, a.raw, method, "/api/v1/operator/"+path, body, 200)
	}
	call("PUT", "limits", `{"limits":{"max_global_runners":7}}`)
	var limits computeLimitsJSON
	if err := json.Unmarshal(call("GET", "limits", "").Body.Bytes(), &limits); err != nil {
		t.Fatal(err)
	}
	if limits.Limits[store.ComputeLimitGlobalRunners] != 7 || limits.Usage.ByPrincipal["other-team"] != 3 {
		t.Fatalf("limits = %+v", limits)
	}
	call("PUT", "signups", `{"mode":"waitlist","reason":"review","hourly_limit":12}`)
	var signup signUpStatusJSON
	if err := json.Unmarshal(call("GET", "signups", "").Body.Bytes(), &signup); err != nil {
		t.Fatal(err)
	}
	if signup.SetBy != "operator-account" || signup.Mode != "waitlist" || signup.Limits.HourlyLimit != 12 || signup.Counts.LastDay != 3 {
		t.Fatalf("signup = %+v", signup)
	}
	call("PUT", "object-store/settings", `{"run_retention_days":7,"node_metric_retention_days":3,"database_alarm_bytes":123}`)
	call("PUT", "object-store/quotas/customer", `{"max_bytes_per_run":22}`)
	if f.quota.Team != "customer" || f.quota.AllowanceBytes != 32 {
		t.Fatalf("quota = %+v", f.quota)
	}
	call("PUT", "object-store/quotas/customer/allowance", `{"storage_allowance_bytes":45}`)
	var storage storageStateJSON
	if err := json.Unmarshal(call("GET", "object-store", "").Body.Bytes(), &storage); err != nil {
		t.Fatal(err)
	}
	if storage.Settings.RunRetentionDays != 7 || len(storage.Quotas) != 1 || storage.Quotas[0].StorageAllowanceBytes != 45 || storage.Quotas[0].MaxBytesPerRun != 22 || len(storage.Teams) != 1 {
		t.Fatalf("storage = %+v", storage)
	}
	if f.writes != 5 {
		t.Fatalf("writes = %d", f.writes)
	}
}

func TestOperatorSettingsHTTPInvalidBodiesDoNotWrite(t *testing.T) {
	s, f, a := newOperatorSettingsFixture(t)
	h := s.Handler()
	for _, tc := range [][2]string{
		{"limits", `{"limits":{"max_global_runners":null}}`},
		{"limits", `{"limits":null}`},
		{"limits", `{"limits":{"max_global_runners":null},"limits":{"runner_alarm":3}}`},
		{"limits", `{"limits":{"max_global_runners":7,"max_run_seconds":-1}}`},
		{"limits", `{"limits":{"unknown":1}}`},
		{"signups", `{}`},
		{"signups", `{"mode":"invalid"}`},
		{"signups", `{"mode":"waitlist","actor":"forged"}`},
		{"object-store/settings", `{"run_retention_days":-1}`},
		{"object-store/settings", `{"default_tier":"unknown"}`},
		{"object-store/quotas/customer", `{"principal":"other-team"}`},
		{"object-store/quotas/customer", `{"max_bytes_per_run":-1}`},
		{"object-store/quotas/customer/allowance", `{"storage_allowance_bytes":-1}`},
	} {
		t.Run(tc[0]+tc[1], func(t *testing.T) { operatorRequest(t, h, a.raw, "PUT", "/api/v1/operator/"+tc[0], tc[1], 400) })
	}
	if f.writes != 0 {
		t.Fatalf("invalid requests wrote %d changes", f.writes)
	}
}

func TestOperatorSettingsHTTPStoreFailuresStayPrivate(t *testing.T) {
	for _, failure := range []error{errors.New("private database details"), &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}} {
		s, f, a := newOperatorSettingsFixture(t)
		f.failure = failure
		h := s.Handler()
		for _, tc := range [][3]string{
			{"GET", "limits", ""},
			{"PUT", "limits", `{"limits":{"max_global_runners":7}}`},
			{"GET", "signups", ""},
			{"PUT", "signups", `{"mode":"waitlist","reason":"review"}`},
			{"GET", "object-store", ""},
			{"PUT", "object-store/settings", `{}`},
			{"PUT", "object-store/quotas/customer", `{}`},
			{"PUT", "object-store/quotas/customer/allowance", `{}`},
		} {
			want := 500
			if errors.As(failure, new(*apierr.Error)) {
				want = 409
			}
			w := operatorRequest(t, h, a.raw, tc[0], "/api/v1/operator/"+tc[1], tc[2], want)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatalf("unsafe error: %s", w.Body.String())
			}
		}
		if f.writes != 0 {
			t.Fatal("failed transaction changed settings")
		}
	}
}
