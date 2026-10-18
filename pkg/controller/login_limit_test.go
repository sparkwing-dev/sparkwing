package controller

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

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

func failedTokenLogin(t *testing.T, srv *Server, h http.Handler, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("token=swu_wrong&csrf_token=pair"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	req.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, srv.cookiesSecure()), Value: "pair"})
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func multiTeamLoginServer(t *testing.T) *Server {
	t.Helper()
	return loginServer(t, nil).WithLicense(accountSessionTestLicense(t))
}

func TestLoginLimiter_IPv6RotationWithinOnePrefixCannotBlockOthers(t *testing.T) {
	srv := multiTeamLoginServer(t)
	h := srv.Handler()
	refused := 0
	for i := range loginGlobalBurst {
		addr := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 2, 0, 0, 0, 0, 0, 0, byte(i / 256), byte(i % 256)})
		if failedTokenLogin(t, srv, h, netip.AddrPortFrom(addr, 5000).String()) == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != loginGlobalBurst-loginClientBurst {
		t.Fatalf("one /64 was refused %d times, want %d", refused, loginGlobalBurst-loginClientBurst)
	}
	for _, other := range []string{"[2001:db8:1:3::1]:5000", "198.51.100.4:5000"} {
		if code := failedTokenLogin(t, srv, h, other); code == http.StatusTooManyRequests {
			t.Fatalf("unrelated client %s was refused", other)
		}
	}
}

func TestLoginLimiter_GlobalBucketChargesOnlyFailedTokenLogins(t *testing.T) {
	srv := multiTeamLoginServer(t)
	h := srv.Handler()
	for i := range loginGlobalBurst + 100 {
		addr := netip.AddrFrom4([4]byte{198, 51, byte(i / 256), byte(i % 256)}).String() + ":5000"
		if rec := postLogin(t, h, addr, "", loginBodyNoCSRF); rec.Code != http.StatusForbidden {
			t.Fatalf("form refusal %d: status = %d, want %d", i+1, rec.Code, http.StatusForbidden)
		}
		oauth := httptest.NewRequest(http.MethodGet, "/auth/github/start", nil)
		oauth.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, oauth)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("OAuth start %d was charged to the listener budget", i+1)
		}
	}
	for i := range loginGlobalBurst {
		addr := netip.AddrFrom4([4]byte{203, 0, byte(i / 256), byte(i % 256)}).String() + ":5000"
		if code := failedTokenLogin(t, srv, h, addr); code == http.StatusTooManyRequests {
			t.Fatalf("failed login %d refused before the listener budget was spent", i+1)
		}
	}
	// safety: the bucket refills while the burst is spent, so allow a few extra before the listener must refuse.
	for i := range 200 {
		addr := netip.AddrFrom4([4]byte{192, 0, byte(i / 256), byte(i % 256)}).String() + ":5000"
		if failedTokenLogin(t, srv, h, addr) == http.StatusTooManyRequests {
			oauth := httptest.NewRequest(http.MethodGet, "/auth/github/start", nil)
			oauth.RemoteAddr = "192.0.2.250:5000"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, oauth)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatal("spent token-login budget refused OAuth start")
			}
			return
		}
	}
	t.Fatal("no fresh client was refused after the failed-login budget was spent")
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
