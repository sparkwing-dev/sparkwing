package acceptance_test

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestConsoleSessionHTTPDirectAccount(t *testing.T) {
	now := time.Now().UTC()
	f := newSessionFlow(t, teststore.WithClock(func() time.Time { return now }))
	f.server.WithOperatorAccounts([]string{"member"}).WithDashboard(controller.Dashboard{})
	h := f.server.Handler()
	for _, header := range []bool{false, true} {
		r := f.request("GET", "/api/v1/operator/teams", "")
		if header {
			r.Header.Set("Authorization", "Session "+f.raw)
			r.Header.Set("Cookie", "__Host-sw_session=foreign")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), f.raw) {
			t.Fatalf("header=%v HTTP %d %s", header, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", "/api/v1/operator/teams/foreign", ""))
	if w.Code != 200 {
		t.Fatalf("deployment selection: HTTP %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", "/api/v1/auth/whoami", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"account"`) {
		t.Fatalf("member identity: HTTP %d %s", w.Code, w.Body.String())
	}
	f.server.WithOperatorAccounts(nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", "/api/v1/operator/teams", ""))
	assertEngineError(t, w, 403, apierr.Forbidden)
}

func TestConsoleSessionHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	calls := 0
	f.server.WithOperatorAccounts([]string{"member"}).WithRunnerMeteringWriter(runnerMeteringWriter(func(context.Context, capability.Authorized, string, bool) error { calls++; return nil }))
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	property := func(value []byte, mutation uint8) bool {
		forged := "forged-" + hex.EncodeToString(value)
		r := f.request("POST", "/api/v1/operator/runners/root/metered", `{"metered":true}`)
		want := 403
		switch mutation % 6 {
		case 0:
			r.Header.Del("X-CSRF-Token")
		case 1:
			r.Header.Set("Origin", "https://foreign.test")
		case 2:
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw+"; __Host-sw_csrf="+forged)
			r.Header.Set("X-CSRF-Token", forged)
		case 3:
			r.Header.Set("Authorization", "Session "+forged)
			want = 401
		case 4:
			r.Header.Set("Authorization", "Bearer "+f.raw)
			want = 401
		case 5:
			r.Header.Add("Authorization", "Session "+f.raw)
			r.Header.Add("Authorization", "Session "+forged)
			want = 401
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == want && calls == 0 && !strings.Contains(w.Body.String(), f.raw) && !strings.Contains(w.Body.String(), f.csrf)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("POST", "/api/v1/operator/runners/root/metered", `{"metered":true}`))
	if w.Code != 200 || calls != 1 {
		t.Fatalf("valid proof: HTTP %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
}

func TestConsoleSessionHTTPRejectsCapabilityAncestry(t *testing.T) {
	f := newSessionFlow(t)
	root := loginCapability(t, f)
	raw, _, err := f.store.AsOperator().CreateCapabilitySession(t.Context(), root.Capability.Prefix, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := f.server.WithOperatorAccounts([]string{"member"}).Handler()
	for _, presentation := range []string{"Bearer " + root.Raw, "Session " + raw} {
		r := f.request("GET", "/api/v1/operator/teams", "")
		r.Header.Set("Authorization", presentation)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertEngineError(t, w, 403, apierr.Forbidden)
	}
}
