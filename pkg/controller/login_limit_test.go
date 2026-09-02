package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func postLogin(t *testing.T, h http.Handler, remoteAddr, forwardedFor, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const loginBodyNoPassword = `{"username":"nobody"}`

func TestLoginLimiter_PerClientBucketReturns429(t *testing.T) {
	h := New(newStoreForAuth(t), nil).Handler()

	for i := range loginClientBurst {
		rec := postLogin(t, h, "203.0.113.7:5000", "", loginBodyNoPassword)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusBadRequest)
		}
	}
	rec := postLogin(t, h, "203.0.113.7:5000", "", loginBodyNoPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after burst = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want %q", got, "60")
	}
	if other := postLogin(t, h, "198.51.100.4:5000", "", loginBodyNoPassword); other.Code != http.StatusBadRequest {
		t.Fatalf("second client status = %d, want %d", other.Code, http.StatusBadRequest)
	}
}

func TestLoginLimiter_GlobalBucketReturns429(t *testing.T) {
	h := New(newStoreForAuth(t), nil).Handler()

	for i := range loginGlobalBurst {
		addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String() + ":5000"
		if rec := postLogin(t, h, addr, "", loginBodyNoPassword); rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusBadRequest)
		}
	}
	rec := postLogin(t, h, "203.0.113.200:5000", "", loginBodyNoPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fresh client after global burst = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestLoginLimiter_TrustedProxySeparatesForwardedClients(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := New(newStoreForAuth(t), nil).WithTrustedProxyCIDRs(trusted).Handler()

	for range loginClientBurst {
		postLogin(t, h, "10.0.0.1:5000", "198.51.100.7", loginBodyNoPassword)
	}
	drained := postLogin(t, h, "10.0.0.1:5000", "198.51.100.7", loginBodyNoPassword)
	if drained.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded client status = %d, want %d", drained.Code, http.StatusTooManyRequests)
	}
	fresh := postLogin(t, h, "10.0.0.1:5000", "198.51.100.8", loginBodyNoPassword)
	if fresh.Code != http.StatusBadRequest {
		t.Fatalf("second forwarded client status = %d, want %d", fresh.Code, http.StatusBadRequest)
	}
}

func TestLoginLimiter_UntrustedPeerCannotRotateWithForwardedHeader(t *testing.T) {
	h := New(newStoreForAuth(t), nil).Handler()

	for i := range loginClientBurst {
		forwarded := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String()
		if rec := postLogin(t, h, "203.0.113.7:5000", forwarded, loginBodyNoPassword); rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusBadRequest)
		}
	}
	rec := postLogin(t, h, "203.0.113.7:5000", "198.51.100.250", loginBodyNoPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; forwarded headers must not mint new budgets", rec.Code, http.StatusTooManyRequests)
	}
}

func TestLogin_AccountFailureBackoff(t *testing.T) {
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	if _, err := st.CreateUser("alice", "correct horse battery", []string{ScopeAdmin}, now); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := st.CreateUser("bob", "correct horse battery", []string{ScopeAdmin}, now); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	h := New(st, nil).Handler()
	wrong := `{"username":"alice","password":"wrong password"}`

	for i := range loginFailureBurst {
		rec := postLogin(t, h, "203.0.113.7:5000", "", wrong)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusUnauthorized)
		}
	}
	rec := postLogin(t, h, "203.0.113.7:5000", "", wrong)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after failure budget = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if !strings.Contains(rec.Body.String(), "this account") {
		t.Fatalf("body = %q, want the per-account message", rec.Body.String())
	}

	ok := postLogin(t, h, "203.0.113.7:5000", "", `{"username":"bob","password":"correct horse battery"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("untouched account status = %d, want %d; backoff must be per account", ok.Code, http.StatusOK)
	}
}

func TestAuthenticator_NegativeCacheShortCircuitsRepeatedGuess(t *testing.T) {
	st := newStoreForAuth(t)
	a := NewAuthenticator(st, time.Minute)
	clock := time.Unix(0, 0).UTC()
	a.now = func() time.Time { return clock }

	guess := store.TokenPrefixUser + "_" + strings.Repeat("a", 32)
	_, first := a.Authenticate(guess)
	if first == nil {
		t.Fatalf("expected the guess to fail")
	}

	// safety: closing the store makes any further lookup fail differently, so a matching error proves the cache answered.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	_, cached := a.Authenticate(guess)
	if cached == nil || cached.Error() != first.Error() {
		t.Fatalf("cached error = %v, want %v", cached, first)
	}

	other := store.TokenPrefixUser + "_" + strings.Repeat("b", 32)
	if _, err := a.Authenticate(other); err == nil || err.Error() == first.Error() {
		t.Fatalf("a different guess must reach the store, got %v", err)
	}

	clock = clock.Add(negativeAuthCacheTTL + time.Second)
	if _, err := a.Authenticate(guess); err == nil || err.Error() == first.Error() {
		t.Fatalf("expired entry must reach the store, got %v", err)
	}
}

func TestAuthenticator_NegativeCacheIsBounded(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), time.Minute)
	now := time.Unix(0, 0).UTC()
	for i := range negativeAuthCacheCap + 100 {
		a.rememberFailure(strings.Repeat("x", 8)+string(rune(i)), errors.New("unknown token"), now)
	}
	if got := a.negCount.Load(); got > negativeAuthCacheCap {
		t.Fatalf("negative cache holds %d entries, want at most %d", got, negativeAuthCacheCap)
	}
}
