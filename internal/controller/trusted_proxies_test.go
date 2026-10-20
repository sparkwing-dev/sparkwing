package controller

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestTrustedProxyCIDRsSeparateClientLimitsAndLogs(t *testing.T) {
	var logs bytes.Buffer
	srv := loginServer(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	srv.WithTrustedProxyCIDRs([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	handler := srv.Handler()
	request := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginBodyNoCSRF))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10.0.0.2:4433"
		req.Header.Set("X-Forwarded-For", "192.0.2.99, "+client+", 10.0.0.1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for range loginClientBurst {
		if got := request("198.51.100.7"); got != http.StatusForbidden {
			t.Fatalf("login = %d", got)
		}
	}
	if got := request("198.51.100.7"); got != http.StatusTooManyRequests {
		t.Fatalf("exhausted client = %d", got)
	}
	if got := request("198.51.100.8"); got != http.StatusForbidden {
		t.Fatalf("fresh client = %d", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "10.0.0.2:4433"
	req.Header.Set("X-Forwarded-For", "198.51.100.8, 10.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(logs.String(), `"client_ip":"198.51.100.8"`) {
		t.Fatalf("forwarded client missing from log: %s", logs.String())
	}
}

func TestTrustedProxyCIDRsRefuseUntrustedHeaderRotation(t *testing.T) {
	var logs bytes.Buffer
	srv := loginServer(t, slog.New(slog.NewJSONHandler(&logs, nil))).WithTrustedProxyCIDRs([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	handler := srv.Handler()
	for i := range loginClientBurst + 1 {
		forwarded := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String()
		rec := postLogin(t, handler, "203.0.113.7:5000", forwarded, loginBodyNoCSRF)
		want := http.StatusForbidden
		if i == loginClientBurst {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("untrusted request %d: %d, want %d", i, rec.Code, want)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:5000"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(logs.String(), `"client_ip":"203.0.113.7"`) {
		t.Fatalf("peer address absent: %s", logs.String())
	}
}
