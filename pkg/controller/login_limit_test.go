package controller

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

	for i := range srv.loginLimit.globalBurst {
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

func TestLoginLimiter_GlobalBucketExceedsAFleetsLogins(t *testing.T) {
	srv := loginServer(t, nil)
	if got := srv.loginLimit.globalBurst; got < 10*loginClientBurst {
		t.Fatalf("global burst = %d, want well above one client's %d so a fleet is not throttled",
			got, loginClientBurst)
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

func TestAuthenticator_NegativeCacheEvictsInsteadOfClosing(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), time.Minute)
	now := time.Unix(0, 0).UTC()
	key := func(i int) string { return strings.Repeat("x", 8) + string(rune(i)) }
	for i := range negativeAuthCacheCap + 100 {
		a.rememberFailure(key(i), store.ErrUnknownToken, now)
	}
	if got := a.negCount.Load(); got > negativeAuthCacheCap {
		t.Fatalf("negative cache holds %d entries, want at most %d", got, negativeAuthCacheCap)
	}
	// safety: a full cache must still take the newest entry, or cheap failures would buy a hash per replayed guess.
	last := key(negativeAuthCacheCap + 99)
	if _, ok := a.recentFailure(last, now); !ok {
		t.Fatalf("the newest failure was refused at the cap instead of evicting an older one")
	}
}

func TestAuthenticator_PrefixBudgetRefusesVariedGuessesWithoutHashing(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	raw, _, err := st.CreateToken("runner", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	a := NewAuthenticator(st, time.Minute)
	clock := now
	a.now = func() time.Time { return clock }
	prefix := raw[:store.PrefixLen]

	for i := range authPrefixFailureBurst {
		guess := prefix + strings.Repeat(string(rune('a'+i%26)), 32)
		if _, err := a.Authenticate(guess); err == nil {
			t.Fatalf("guess %d unexpectedly authenticated", i+1)
		}
	}

	// safety: closing the store makes any lookup fail differently, so a budget answer proves nothing was hashed.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	_, err = a.Authenticate(prefix + strings.Repeat("z", 32))
	if !errors.Is(err, errAuthThrottled) {
		t.Fatalf("error = %v, want the prefix budget to refuse before hashing", err)
	}

	rec := httptest.NewRecorder()
	a.writeAuthFailure(rec, err)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatalf("Retry-After is empty on a throttled bearer")
	}

	clock = clock.Add(authPrefixFailureWindow + time.Second)
	if _, err := a.Authenticate(prefix + strings.Repeat("y", 32)); errors.Is(err, errAuthThrottled) {
		t.Fatalf("the budget must refill, got %v", err)
	}
}

func TestAuthenticator_UnmatchedPrefixDoesNotSpendTheBudget(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), time.Minute)
	now := time.Unix(0, 0).UTC()
	a.now = func() time.Time { return now }

	for i := range authPrefixFailureBurst * 3 {
		guess := store.TokenPrefixUser + "_nobody" + string(rune('a'+i%26)) + strings.Repeat("q", 24)
		if _, err := a.Authenticate(guess); errors.Is(err, errAuthThrottled) {
			t.Fatalf("guess %d hit the budget, but no stored hash was compared", i+1)
		}
	}
}

func TestAuthenticator_NegativeCacheWorksWithoutAPositiveCache(t *testing.T) {
	st := newStoreForAuth(t)
	a := NewAuthenticator(st, 0)
	now := time.Unix(0, 0).UTC()
	a.now = func() time.Time { return now }

	guess := store.TokenPrefixUser + "_" + strings.Repeat("a", 32)
	if _, err := a.Authenticate(guess); err == nil {
		t.Fatalf("expected the guess to fail")
	}
	if got := a.negCount.Load(); got != 1 {
		t.Fatalf("negative cache holds %d entries, want 1 with the positive cache off", got)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := a.Authenticate(guess); !errors.Is(err, store.ErrNoTokenCandidates) {
		t.Fatalf("error = %v, want the cached rejection", err)
	}
}

func TestAuthenticator_StoreErrorIsNotCached(t *testing.T) {
	st := newStoreForAuth(t)
	a := NewAuthenticator(st, time.Minute)
	now := time.Unix(0, 0).UTC()
	a.now = func() time.Time { return now }

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	guess := store.TokenPrefixUser + "_" + strings.Repeat("a", 32)
	if _, err := a.Authenticate(guess); err == nil {
		t.Fatalf("expected the closed store to fail the lookup")
	}
	if got := a.negCount.Load(); got != 0 {
		t.Fatalf("negative cache holds %d entries, want 0; a store error must not be replayed", got)
	}

	a.rememberFailure("swu_busy", store.ErrHashingBusy, now)
	if got := a.negCount.Load(); got != 0 {
		t.Fatalf("a saturation error was cached")
	}

	rec := httptest.NewRecorder()
	a.writeAuthFailure(rec, errors.New("tokens: query: sql: database is closed"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d for a store error", rec.Code, http.StatusServiceUnavailable)
	}
	if body := rec.Body.String(); strings.Contains(body, "sql") {
		t.Fatalf("body = %q, want no store detail for an unauthenticated caller", body)
	}
}

func TestAuthenticator_HashingBusyIs503(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), time.Minute)
	rec := httptest.NewRecorder()
	a.writeAuthFailure(rec, store.ErrHashingBusy)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want %q", got, "1")
	}
}

func TestAuthenticator_CachedPrincipalNeedsNoStoreOrHash(t *testing.T) {
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	raw, _, err := st.CreateToken("runner", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	a := NewAuthenticator(st, time.Minute)
	if _, err := a.Authenticate(raw); err != nil {
		t.Fatalf("cold authenticate: %v", err)
	}
	// safety: a closed store fails every lookup, so a second success proves the heartbeat path skips store and semaphore.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	p, err := a.Authenticate(raw)
	if err != nil {
		t.Fatalf("warm authenticate: %v", err)
	}
	if p.Name != "runner" {
		t.Fatalf("principal = %q, want %q", p.Name, "runner")
	}
}

func TestAuthenticator_PrefixBudgetIsPerClient(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	raw, _, err := st.CreateToken("runner", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	a := NewAuthenticator(st, time.Minute)
	h := a.Middleware(teapotHandler())
	prefix := raw[:store.PrefixLen]

	bearer := func(token, remoteAddr string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := range authPrefixFailureBurst {
		guess := prefix + strings.Repeat(string(rune('a'+i%26)), 32)
		if code := bearer(guess, "203.0.113.7:5000"); code != http.StatusUnauthorized {
			t.Fatalf("guess %d: status = %d, want %d", i+1, code, http.StatusUnauthorized)
		}
	}
	if code := bearer(prefix+strings.Repeat("z", 32), "203.0.113.7:5000"); code != http.StatusTooManyRequests {
		t.Fatalf("guesser status = %d, want %d", code, http.StatusTooManyRequests)
	}

	// safety: the real runner holds a cold cache here, so a drained stranger budget must not deny its own token.
	if code := bearer(raw, "198.51.100.4:5000"); code != http.StatusTeapot {
		t.Fatalf("runner status = %d, want %d; a stranger must not lock a token prefix out", code, http.StatusTeapot)
	}
}
