package wingd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
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
)

// DashboardLogin is the one-time sign-in link api.sock returns.
type DashboardLogin struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// safety: codes and sessions live only in this controller run, so every restart revokes every sign-in; a process
// that held the dashboard port while the controller was down cannot replay a token it collected.
type dashboardLogins struct {
	now     func() time.Time
	mu      sync.Mutex
	origin  string
	origins map[string]string
	entries map[string]dashboardEntry
	watch   map[string]*sessionWatch
	// hack: tests interpose a revocation between a request's session lookup and serving it.
	afterLookup func(digest string)
}

// safety: entries are keyed by the SHA-256 digest of their token, so the map never holds a usable credential.
type dashboardEntry struct {
	code    bool
	origin  string
	peer    localhostwire.LocalPeerIdentity
	expires time.Time
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
	return &dashboardLogins{now: now, entries: map[string]dashboardEntry{}, watch: map[string]*sessionWatch{}}
}

func (l *dashboardLogins) serving(origin string, allowedHosts []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.origin = normalizeDashboardOrigin(origin)
	l.origins = make(map[string]string, len(allowedHosts))
	for _, host := range allowedHosts {
		host = strings.ToLower(host)
		l.origins[host] = "https://" + host
	}
}

func (l *dashboardLogins) allowedOrigins() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	origins := make([]string, 0, len(l.origins))
	for _, origin := range l.origins {
		origins = append(origins, origin)
	}
	return origins
}

func (l *dashboardLogins) servingOrigin() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.origin
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
		login, failure := l.mint(peer, r.URL.Query().Get("host"))
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

func (l *dashboardLogins) mint(peer localhostwire.LocalPeerIdentity, host string) (DashboardLogin, *apierr.Error) {
	token, err := randomToken()
	if err != nil {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Internal, Message: "dashboard sign-in token: " + err.Error()}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.origin == "" {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Unavailable, Message: "the dashboard is not serving", Hint: "run sparkwing dashboard status to see why"}
	}
	origin := l.origin
	if host != "" {
		origin = l.origins[strings.ToLower(host)]
		if origin == "" {
			return DashboardLogin{}, &apierr.Error{Code: apierr.BadRequest, Message: "dashboard host is not configured", Hint: "add its exact DNS name to dashboard.allowed_hosts in config.yaml, then run sparkwing daemon restart --force"}
		}
	}
	now := l.now()
	for digest, entry := range l.entries {
		if !now.Before(entry.expires) {
			delete(l.entries, digest)
		}
	}
	expires := now.Add(dashboardLoginLifetime)
	l.entries[tokenDigest(token)] = dashboardEntry{code: true, origin: origin, peer: peer.Clone(), expires: expires}
	// safety: the code rides in the fragment, which the browser never sends; the page exchanges it
	// and drops it from history.
	return DashboardLogin{URL: origin + "/#code=" + token, ExpiresAt: expires}, nil
}

// safety: the code is spent even when it is refused, so a guess at another origin uses it up.
func (l *dashboardLogins) redeem(code, session, origin string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[code]
	delete(l.entries, code)
	now := l.now()
	if !ok || !entry.code || entry.origin != origin || !now.Before(entry.expires) {
		return time.Time{}, false
	}
	expires := now.Add(dashboardSessionLifetime)
	l.entries[session] = dashboardEntry{origin: origin, peer: entry.peer, expires: expires}
	return expires, true
}

func (l *dashboardLogins) revoke(digest string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[digest]
	if !ok || entry.code {
		return false
	}
	delete(l.entries, digest)
	return l.now().Before(entry.expires)
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normalizeDashboardOrigin(origin string) string {
	parsed, err := url.Parse(origin)
	if err == nil && parsed.Scheme == "http" && parsed.Port() == "80" {
		parsed.Host = strings.TrimSuffix(parsed.Host, ":80")
		return parsed.String()
	}
	return origin
}

// safety: the origin guard has already refused a foreign Host, so this is the origin the browser addressed.
func (l *dashboardLogins) requestOrigin(r *http.Request) string {
	l.mu.Lock()
	origin := l.origins[strings.ToLower(r.Host)]
	l.mu.Unlock()
	if origin != "" {
		return origin
	}
	return normalizeDashboardOrigin("http://" + r.Host)
}

// safety: only the Authorization header authenticates. A browser attaches it from script on this origin alone,
// unlike a cookie, which every port on the host receives.
func sessionToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && token != "" && !strings.ContainsAny(token, " \t")
}

type dashboardSession struct {
	digest  string
	peer    localhostwire.LocalPeerIdentity
	expires time.Time
}

// safety: the caller watches the digest before this lookup, and Log out revokes before it ends watches, so a
// request validated just before a revocation is still ended by it.
func (l *dashboardLogins) lookup(r *http.Request, digest string) (dashboardSession, bool) {
	l.mu.Lock()
	entry, ok := l.entries[digest]
	hook := l.afterLookup
	l.mu.Unlock()
	if hook != nil {
		hook(digest)
	}
	// safety: a session redeemed at one origin does not authorize another origin on the same host.
	if !ok || entry.code || !l.now().Before(entry.expires) || entry.origin != l.requestOrigin(r) {
		return dashboardSession{}, false
	}
	return dashboardSession{digest: digest, peer: entry.peer.Clone(), expires: entry.expires}, true
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
		case "sw_session", "__Host-sw_session":
		default:
			request.AddCookie(cookie)
		}
	}
	next.ServeHTTP(w, request)
}

// safety: sign-in and authenticated writes must name this session's origin, including the proxy's configured scheme.
func (l *dashboardLogins) sameDashboardOrigin(r *http.Request) bool {
	return normalizeDashboardOrigin(r.Header.Get("Origin")) == l.requestOrigin(r)
}

func unsafeDashboardMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// safety: a browser acts as the api.sock peer that minted its link only while it sends that session's
// bearer. The static shell carries no run data and loads signed out; it exchanges the sign-in code.
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
		if token, ok := sessionToken(r); ok {
			digest := tokenDigest(token)
			ended, release := l.watchSession(digest)
			if current, ok := l.lookup(r, digest); ok {
				defer release()
				if unsafeDashboardMethod(r.Method) && !l.sameDashboardOrigin(r) {
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

type dashboardExchange struct {
	Code string `json:"code"`
}

type dashboardSessionGrant struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (l *dashboardLogins) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "the dashboard page exchanges its sign-in code with POST"})
		return
	}
	if !l.sameDashboardOrigin(r) {
		http.Error(w, "forbidden: exchange a sign-in code from the dashboard's own origin", http.StatusForbidden)
		return
	}
	var exchange dashboardExchange
	refused := &apierr.Error{Code: apierr.Unauthenticated, Message: "this sign-in link is invalid, expired, already used or for another address", Hint: "run sparkwing dashboard open for a new link"}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&exchange) != nil || exchange.Code == "" {
		writeLoginFailure(w, refused)
		return
	}
	session, err := randomToken()
	if err != nil {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Internal, Message: "dashboard session token: " + err.Error()})
		return
	}
	expires, ok := l.redeem(tokenDigest(exchange.Code), tokenDigest(session), l.requestOrigin(r))
	if !ok {
		writeLoginFailure(w, refused)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(dashboardSessionGrant{Token: session, ExpiresAt: expires}); err != nil {
		panic(http.ErrAbortHandler) //nolint:forbidigo // net/http aborts the incomplete response.
	}
}

func (l *dashboardLogins) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "sign out with POST"})
		return
	}
	if !l.sameDashboardOrigin(r) {
		http.Error(w, "forbidden: sign out from the dashboard's own origin", http.StatusForbidden)
		return
	}
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4096)); err != nil {
		http.Error(w, "invalid sign-out body", http.StatusBadRequest)
		return
	}
	token, ok := sessionToken(r)
	live := false
	if ok {
		digest := tokenDigest(token)
		live = l.revoke(digest)
		l.end(digest)
	}
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
