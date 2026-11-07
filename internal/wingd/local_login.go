package wingd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

// DashboardLoginPath is the api.sock route that mints a one-time dashboard sign-in link.
const DashboardLoginPath = "/api/v1/local/dashboard/login"

const (
	dashboardLoginLifetime   = 60 * time.Second
	dashboardSessionLifetime = 30 * 24 * time.Hour
	dashboardCookie          = "sw_dashboard"
	// safety: the marker only tells the page to offer sign-out; the gate never reads it.
	dashboardSignedInMarker = "sw_dashboard_signed_in"
)

// DashboardLogin is the one-time sign-in link api.sock returns.
type DashboardLogin struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// DashboardSessionStore persists sign-in codes and browser sessions by SHA-256 digest.
// RedeemDashboardCode spends the code even when it refuses it.
type DashboardSessionStore interface {
	MintDashboardCode(ctx context.Context, digest, origin string, peer localhostwire.LocalPeerIdentity, now, expires time.Time) error
	RedeemDashboardCode(ctx context.Context, code, session, origin string, now, expires time.Time) (localhostwire.LocalPeerIdentity, error)
	// DashboardSession reports false for an unknown, expired or revoked digest.
	DashboardSession(ctx context.Context, digest string, now time.Time) (localhostwire.LocalPeerIdentity, string, time.Time, bool, error)
	RevokeDashboardSession(ctx context.Context, digest string, now time.Time) (bool, error)
}

type dashboardLogins struct {
	now    func() time.Time
	mu     sync.Mutex
	origin string
	store  DashboardSessionStore
	watch  map[string]*sessionWatch
}

// safety: Log out closes ended to stop every request the session is still serving, including open streams.
type sessionWatch struct {
	ended chan struct{}
	refs  int
}

func newDashboardLogins(now func() time.Time) *dashboardLogins {
	if now == nil {
		now = time.Now
	}
	return &dashboardLogins{now: now, watch: map[string]*sessionWatch{}}
}

func (l *dashboardLogins) serving(origin string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.origin = origin
}

func (l *dashboardLogins) servingOrigin() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.origin
}

func (l *dashboardLogins) persist(store DashboardSessionStore) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
}

func (l *dashboardLogins) sessions() (DashboardSessionStore, *apierr.Error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		return nil, &apierr.Error{Code: apierr.Unavailable, Message: "dashboard sessions are unavailable in this controller"}
	}
	return l.store, nil
}

func (l *dashboardLogins) socketRoutes(api http.Handler) http.Handler {
	if api == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DashboardLoginPath {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "dashboard sign-in links are minted with POST"})
			return
		}
		peer, ok := localhostwire.LocalPeer(r.Context())
		if !ok {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "dashboard sign-in links need a verified local peer"})
			return
		}
		login, failure := l.mint(r.Context(), peer)
		if failure != nil {
			writeLoginFailure(w, failure)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(login); err != nil {
			panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
		}
	})
}

func (l *dashboardLogins) mint(ctx context.Context, peer localhostwire.LocalPeerIdentity) (DashboardLogin, *apierr.Error) {
	origin := l.servingOrigin()
	if origin == "" {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Unavailable, Message: "the dashboard is not serving", Hint: "run sparkwing dashboard status to see why"}
	}
	store, failure := l.sessions()
	if failure != nil {
		return DashboardLogin{}, failure
	}
	token, err := randomToken()
	if err != nil {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Internal, Message: "dashboard sign-in token: " + err.Error()}
	}
	now := l.now()
	expires := now.Add(dashboardLoginLifetime)
	if err := store.MintDashboardCode(ctx, tokenDigest(token), origin, peer.Clone(), now, expires); err != nil {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Internal, Message: "store dashboard sign-in code: " + err.Error()}
	}
	return DashboardLogin{URL: origin + "/login?" + url.Values{"token": {token}}.Encode(), ExpiresAt: expires}, nil
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// safety: the origin guard has already refused a foreign Host, so this is the origin the browser addressed.
func requestOrigin(r *http.Request) string {
	return "http://" + r.Host
}

// safety: one cookie of our name is required; a duplicate planted by another service on this host is refused.
func sessionToken(r *http.Request, name string) (string, bool) {
	var value string
	for _, cookie := range r.Cookies() {
		if cookie.Name != name {
			continue
		}
		if value != "" || cookie.Value == "" {
			return "", false
		}
		value = cookie.Value
	}
	return value, value != ""
}

type dashboardSession struct {
	digest  string
	peer    localhostwire.LocalPeerIdentity
	expires time.Time
}

// safety: the caller watches the digest before this lookup, and Log out revokes in the store before it ends
// watches, so a request validated just before a revocation is still ended by it.
func (l *dashboardLogins) lookup(r *http.Request, digest string) (dashboardSession, *apierr.Error) {
	store, failure := l.sessions()
	if failure != nil {
		return dashboardSession{}, failure
	}
	peer, stored, expires, live, err := store.DashboardSession(r.Context(), digest, l.now())
	if err != nil {
		return dashboardSession{}, &apierr.Error{Code: apierr.Unavailable, Message: "read dashboard session: " + err.Error()}
	}
	// safety: a session redeemed at one origin does not authorize another origin on the same host.
	if !live || stored != requestOrigin(r) {
		return dashboardSession{}, nil
	}
	return dashboardSession{digest: digest, peer: peer, expires: expires}, nil
}

func (l *dashboardLogins) watchSession(digest string) (<-chan struct{}, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, ok := l.watch[digest]
	if !ok {
		current = &sessionWatch{ended: make(chan struct{})}
		l.watch[digest] = current
	}
	current.refs++
	return current.ended, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		current.refs--
		// safety: an unknown or finished digest leaves no entry behind, so forged cookies cannot grow the map.
		if current.refs == 0 && l.watch[digest] == current {
			delete(l.watch, digest)
		}
	}
}

func (l *dashboardLogins) end(digest string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.watch[digest]; ok {
		close(current.ended)
		delete(l.watch, digest)
	}
}

func (l *dashboardLogins) serveSession(w http.ResponseWriter, r *http.Request, next http.Handler, current dashboardSession, ended <-chan struct{}) {
	ctx, cancel := context.WithTimeout(localhostwire.WithLocalPeer(r.Context(), current.peer), current.expires.Sub(l.now()))
	defer cancel()
	go func() {
		select {
		case <-ended:
			cancel()
		case <-ctx.Done():
		}
	}()
	request := r.Clone(ctx)
	// safety: the dashboard session proves this peer, not a controller capability, so no credential travels on.
	request.Header.Del("Authorization")
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		switch cookie.Name {
		case dashboardCookie, dashboardSignedInMarker, "sw_session", "__Host-sw_session":
		default:
			request.AddCookie(cookie)
		}
	}
	next.ServeHTTP(w, request)
}

// safety: a cookie-authenticated write must name this dashboard's own origin; a missing, null or foreign Origin is
// refused, because SameSite alone does not stop another page on the same host.
func sameDashboardOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == requestOrigin(r)
}

func unsafeDashboardMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// safety: a browser acts as the api.sock peer that minted its link only while it holds that session's
// cookie. The static shell carries no run data and loads signed out.
func (l *dashboardLogins) gate(next, shell http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			l.login(w, r)
			return
		case "/logout":
			l.logout(w, r)
			return
		}
		if token, ok := sessionToken(r, dashboardCookie); ok {
			digest := tokenDigest(token)
			ended, release := l.watchSession(digest)
			current, failure := l.lookup(r, digest)
			if failure != nil {
				release()
				w.Header().Set("Cache-Control", "no-store")
				writeLoginFailure(w, failure)
				return
			}
			if current.digest != "" {
				defer release()
				if unsafeDashboardMethod(r.Method) && !sameDashboardOrigin(r) {
					http.Error(w, "forbidden: a dashboard write must come from the dashboard's own origin", http.StatusForbidden)
					return
				}
				l.serveSession(w, r, next, current, ended)
				return
			}
			release()
		}
		if shell != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") {
			shell.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "the local dashboard needs a signed-in browser", Hint: "run sparkwing dashboard open and visit the link it prints"})
	})
}

func (l *dashboardLogins) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "open the sign-in link in a browser"})
		return
	}
	store, failure := l.sessions()
	if failure != nil {
		writeLoginFailure(w, failure)
		return
	}
	code := r.URL.Query().Get("token")
	session, err := randomToken()
	if code == "" || err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "this sign-in link is invalid, expired or already used", Hint: "run sparkwing dashboard open for a new link"})
		return
	}
	origin, now := requestOrigin(r), l.now()
	expires := now.Add(dashboardSessionLifetime)
	if _, err := store.RedeemDashboardCode(r.Context(), tokenDigest(code), tokenDigest(session), origin, now, expires); err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "this sign-in link is invalid, expired, already used or for another address", Hint: "run sparkwing dashboard open for a new link"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: dashboardCookie, Value: session, Path: "/", Expires: expires, MaxAge: int(dashboardSessionLifetime / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: dashboardSignedInMarker, Value: "1", Path: "/", Expires: expires, MaxAge: int(dashboardSessionLifetime / time.Second),
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (l *dashboardLogins) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "sign out with POST"})
		return
	}
	if !sameDashboardOrigin(r) {
		http.Error(w, "forbidden: sign out from the dashboard's own origin", http.StatusForbidden)
		return
	}
	store, failure := l.sessions()
	if failure != nil {
		writeLoginFailure(w, failure)
		return
	}
	token, ok := sessionToken(r, dashboardCookie)
	live := false
	if ok {
		digest := tokenDigest(token)
		var err error
		// safety: revoke before clearing cookies, so a failed revocation leaves the browser able to retry.
		if live, err = store.RevokeDashboardSession(r.Context(), digest, l.now()); err != nil {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unavailable, Message: "revoke dashboard session: " + err.Error(), Hint: "try Log out again; the session stays valid until it is revoked"})
			return
		}
		l.end(digest)
	}
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: dashboardSignedInMarker, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteStrictMode})
	if !live {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "this browser is not signed in"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLoginFailure(w http.ResponseWriter, failure *apierr.Error) {
	if err := apierrhttp.Write(w, failure); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
	}
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
