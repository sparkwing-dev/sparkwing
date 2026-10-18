package controller

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// safety: the post carries no CSRF token, so a request the limiter admits answers 403 and never signs in.
func postLogin(t *testing.T, h http.Handler, remoteAddr, forwardedFor, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Real-IP", forwardedFor)
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loginServer(t *testing.T, logger *slog.Logger) *Server {
	t.Helper()
	return New(newStoreForAuth(t), logger).WithDashboard(Dashboard{})
}

const loginBodyNoCSRF = "token=swu_nothing"

func TestLoginListenerRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := newLoginLimiter()
		for i := range 2400 {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = netip.AddrFrom4([4]byte{198, 51, byte(i / 256), byte(i % 256)}).String() + ":5000"
			if err := limiter.admit(req); err != nil {
				t.Fatalf("login %d refused: %v", i+1, err)
			}
		}
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "203.0.113.7:5000"
		if err := limiter.admit(req); err == nil {
			t.Fatal("listener admitted login 2401")
		}
	})
}

func TestLoginLimiter_PerClientBucketReturns429(t *testing.T) {
	h := loginServer(t, nil).Handler()

	for i := range loginClientBurst {
		rec := postLogin(t, h, "203.0.113.7:5000", "", loginBodyNoCSRF)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusForbidden)
		}
	}
	rec := postLogin(t, h, "203.0.113.7:5000", "", loginBodyNoCSRF)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after burst = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want %q", got, "60")
	}
	if other := postLogin(t, h, "198.51.100.4:5000", "", loginBodyNoCSRF); other.Code != http.StatusForbidden {
		t.Fatalf("second client status = %d, want %d", other.Code, http.StatusForbidden)
	}
}

func TestLoginLimiter_GlobalBucketReturns429(t *testing.T) {
	srv := loginServer(t, nil)
	h := srv.Handler()

	for i := range loginGlobalBurst {
		addr := netip.AddrFrom4([4]byte{198, 51, byte(i / 256), byte(i % 256)}).String() + ":5000"
		if rec := postLogin(t, h, addr, "", loginBodyNoCSRF); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusForbidden)
		}
	}
	// safety: the bucket refills while the burst is spent, so allow a few extra before the listener must refuse.
	for i := range 100 {
		addr := netip.AddrFrom4([4]byte{203, 0, byte(i / 256), byte(i % 256)}).String() + ":5000"
		if rec := postLogin(t, h, addr, "", loginBodyNoCSRF); rec.Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatalf("no fresh client was refused after the listener-wide burst")
}

func TestLoginLimiter_TrustedListenerClientsKeepSeparateBudgets(t *testing.T) {
	h := ratelimit.TrustedListener(loginServer(t, nil).Handler())

	for range loginClientBurst {
		postLogin(t, h, "10.0.0.1:5000", "198.51.100.7", loginBodyNoCSRF)
	}
	drained := postLogin(t, h, "10.0.0.1:5000", "198.51.100.7", loginBodyNoCSRF)
	if drained.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded client status = %d, want %d", drained.Code, http.StatusTooManyRequests)
	}
	fresh := postLogin(t, h, "10.0.0.1:5000", "198.51.100.8", loginBodyNoCSRF)
	if fresh.Code != http.StatusForbidden {
		t.Fatalf("second forwarded client status = %d, want %d", fresh.Code, http.StatusForbidden)
	}
}

func TestLoginLimiter_PlainListenerCannotRotateWithForwardedHeaders(t *testing.T) {
	h := loginServer(t, nil).Handler()
	for i := range loginClientBurst {
		forwarded := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String()
		if rec := postLogin(t, h, "203.0.113.7:5000", forwarded, loginBodyNoCSRF); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusForbidden)
		}
	}
	rec := postLogin(t, h, "203.0.113.7:5000", "198.51.100.250", loginBodyNoCSRF)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; forwarded headers must not mint new budgets", rec.Code, http.StatusTooManyRequests)
	}
}

func TestRequestLogRecordsTheTrustedListenersClientAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
		want    string
	}{
		{name: "trusted listener", trusted: true, want: "198.51.100.7"},
		{name: "plain listener", want: "203.0.113.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := loginServer(t, slog.New(slog.NewJSONHandler(&logs, nil))).Handler()
			if tc.trusted {
				h = ratelimit.TrustedListener(h)
			}
			postLogin(t, h, "203.0.113.7:5000", "198.51.100.7", loginBodyNoCSRF)
			if want := `"client_ip":"` + tc.want + `"`; !strings.Contains(logs.String(), want) {
				t.Fatalf("request log lacks %s:\n%s", want, logs.String())
			}
		})
	}
}
