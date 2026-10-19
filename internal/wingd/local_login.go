package wingd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// DashboardLoginPath is the api.sock route that mints a one-time dashboard sign-in link.
const DashboardLoginPath = "/api/v1/local/dashboard/login"

const (
	dashboardLoginLifetime   = 60 * time.Second
	dashboardSessionIdle     = 12 * time.Hour
	dashboardSessionLifetime = 24 * time.Hour
)

// DashboardLogin is the one-time sign-in link api.sock returns.
type DashboardLogin struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pendingLogin struct {
	peer    LocalPeerIdentity
	expires time.Time
}

type dashboardSession struct {
	peer              LocalPeerIdentity
	lastSeen, expires time.Time
}

func (s dashboardSession) live(now time.Time) bool {
	return now.Before(s.lastSeen.Add(dashboardSessionIdle)) && now.Before(s.expires)
}

type dashboardLogins struct {
	now      func() time.Time
	mu       sync.Mutex
	origin   string
	pending  map[[sha256.Size]byte]pendingLogin
	sessions map[[sha256.Size]byte]dashboardSession
}

func newDashboardLogins(now func() time.Time) *dashboardLogins {
	if now == nil {
		now = time.Now
	}
	return &dashboardLogins{now: now, pending: map[[sha256.Size]byte]pendingLogin{}, sessions: map[[sha256.Size]byte]dashboardSession{}}
}

func (l *dashboardLogins) serving(origin string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.origin = origin
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
		peer, ok := LocalPeer(r.Context())
		if !ok {
			writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "dashboard sign-in links need a verified local peer"})
			return
		}
		login, failure := l.mint(peer)
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

func (l *dashboardLogins) mint(peer LocalPeerIdentity) (DashboardLogin, *apierr.Error) {
	token, err := randomToken()
	if err != nil {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Internal, Message: "dashboard sign-in token: " + err.Error()}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.origin == "" {
		return DashboardLogin{}, &apierr.Error{Code: apierr.Unavailable, Message: "the dashboard is not serving", Hint: "run sparkwing dashboard start"}
	}
	now := l.now()
	for key, login := range l.pending {
		if !now.Before(login.expires) {
			delete(l.pending, key)
		}
	}
	expires := now.Add(dashboardLoginLifetime)
	l.pending[sha256.Sum256([]byte(token))] = pendingLogin{peer: copyPeerIdentity(peer), expires: expires}
	return DashboardLogin{URL: l.origin + "/login?" + url.Values{"token": {token}}.Encode(), ExpiresAt: expires}, nil
}

func (l *dashboardLogins) redeem(token string) (string, bool) {
	session, err := randomToken()
	if err != nil {
		return "", false
	}
	key := sha256.Sum256([]byte(token))
	l.mu.Lock()
	defer l.mu.Unlock()
	login, ok := l.pending[key]
	delete(l.pending, key)
	now := l.now()
	if !ok || !now.Before(login.expires) {
		return "", false
	}
	for key, current := range l.sessions {
		if !current.live(now) {
			delete(l.sessions, key)
		}
	}
	l.sessions[sha256.Sum256([]byte(session))] = dashboardSession{peer: login.peer, lastSeen: now, expires: now.Add(dashboardSessionLifetime)}
	return session, true
}

func sessionKey(r *http.Request) ([sha256.Size]byte, bool) {
	session, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || session == "" {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(session)), true
}

func (l *dashboardLogins) session(r *http.Request) (LocalPeerIdentity, bool) {
	key, ok := sessionKey(r)
	if !ok {
		return LocalPeerIdentity{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	current, ok := l.sessions[key]
	now := l.now()
	if !ok || !current.live(now) {
		delete(l.sessions, key)
		return LocalPeerIdentity{}, false
	}
	current.lastSeen = now
	l.sessions[key] = current
	return copyPeerIdentity(current.peer), true
}

// safety: a browser acts as the api.sock peer that minted its link only while it sends that session as a
// bearer. Cookies reach every port on a loopback host, so none is read. The static shell carries no run data and
// loads signed out, because a navigation cannot send the header.
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
		if peer, ok := l.session(r); ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), localPeerKey{}, peer)))
			return
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
	session, ok := l.redeem(r.URL.Query().Get("token"))
	if !ok {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "this sign-in link is invalid, expired or already used", Hint: "run sparkwing dashboard open for a new link"})
		return
	}
	http.Redirect(w, r, "/#s="+session, http.StatusSeeOther)
}

func (l *dashboardLogins) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeLoginFailure(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "sign out with POST"})
		return
	}
	key, ok := sessionKey(r)
	l.mu.Lock()
	current, found := l.sessions[key]
	delete(l.sessions, key)
	live := ok && found && current.live(l.now())
	l.mu.Unlock()
	if !live {
		writeLoginFailure(w, &apierr.Error{Code: apierr.Unauthenticated, Message: "this browser is not signed in"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLoginFailure(w http.ResponseWriter, failure *apierr.Error) {
	if err := apierr.WriteHTTP(w, failure); err != nil {
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
