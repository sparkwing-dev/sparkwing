package localws

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// SignInCodePath mints a single-use browser sign-in code. It answers
	// only the serve token as a bearer, so only the account that can read
	// the token file mints one: POST it and read {"code": ...}.
	SignInCodePath = "/api/v1/local/sign-in-code"
	// SessionExchangePath trades a sign-in code for a session credential
	// the dashboard sends as a bearer: POST {"code": ...} and read
	// {"session": ...}.
	SessionExchangePath = "/auth/local/session"

	signInCodeTTL      = 5 * time.Minute
	maxPendingCodes    = 64
	sessionPrefix      = "swl1."
	sessionMACInfo     = "sparkwing serve session\x00"
	sessionIDBytes     = 16
	signInCodeBytes    = 32
	maxExchangeRequest = 4 << 10
)

// safety: loopback is not a user boundary, so every account on this machine reaches the listener. The serve token
// file is readable only by the account that started the dashboard, and a browser session is a MAC under that token,
// so each credential traces back to the file. No credential rides a cookie: another program on another loopback
// port shares the host a cookie is scoped to.
type serveGate struct {
	token []byte

	mu      sync.Mutex
	pending map[string]time.Time
}

func requireServeToken(next http.Handler, token string) http.Handler {
	g := &serveGate{token: []byte(token), pending: map[string]time.Time{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/version":
			next.ServeHTTP(w, r)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/webhooks/"):
			// safety: a webhook is checked against the signature its sender made.
			next.ServeHTTP(w, r)
			return
		case r.URL.Path == SessionExchangePath:
			g.exchange(w, r)
			return
		case r.URL.Path == SignInCodePath:
			g.mint(w, r)
			return
		}
		if bearer := bearerOf(r); g.isToken(bearer) || g.isSession(bearer) {
			next.ServeHTTP(w, r)
			return
		}
		// safety: the dashboard shell and its docs carry no run data, so a
		// browser loads them signed out and signs in from the page.
		if !strings.HasPrefix(r.URL.Path, "/api/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "unauthorized: open the dashboard link `sparkwing serve status` prints, "+
			"or send the serve-token file in the Sparkwing home as a bearer", http.StatusUnauthorized)
	})
}

func (g *serveGate) mint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || !g.isToken(bearerOf(r)) {
		http.Error(w, "unauthorized: a sign-in code needs the serve token", http.StatusUnauthorized)
		return
	}
	code := randomHex(signInCodeBytes)
	now := time.Now()
	g.mu.Lock()
	for c, expires := range g.pending {
		if now.After(expires) {
			delete(g.pending, c)
		}
	}
	for c := range g.pending {
		if len(g.pending) < maxPendingCodes {
			break
		}
		delete(g.pending, c)
	}
	g.pending[code] = now.Add(signInCodeTTL)
	g.mu.Unlock()
	writeJSON(w, map[string]any{"code": code, "expires_at": now.Add(signInCodeTTL).UTC()})
}

func (g *serveGate) exchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxExchangeRequest))
	if err != nil || json.Unmarshal(raw, &body) != nil || !g.spend(body.Code) {
		http.Error(w, "unauthorized: the sign-in code is unknown, used or expired; "+
			"open the link `sparkwing serve status` prints again", http.StatusUnauthorized)
		return
	}
	id := randomHex(sessionIDBytes)
	writeJSON(w, map[string]string{"session": sessionPrefix + id + "." + g.sessionMAC(id)})
}

func (g *serveGate) spend(code string) bool {
	if code == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	expires, ok := g.pending[code]
	delete(g.pending, code)
	return ok && time.Now().Before(expires)
}

func (g *serveGate) isToken(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), g.token) == 1
}

func (g *serveGate) isSession(got string) bool {
	rest, ok := strings.CutPrefix(got, sessionPrefix)
	if !ok {
		return false
	}
	id, mac, ok := strings.Cut(rest, ".")
	if !ok || id == "" {
		return false
	}
	return hmac.Equal([]byte(mac), []byte(g.sessionMAC(id)))
}

func (g *serveGate) sessionMAC(id string) string {
	h := hmac.New(sha256.New, g.token)
	_, _ = h.Write([]byte(sessionMACInfo + id))
	return hex.EncodeToString(h.Sum(nil))
}

func bearerOf(r *http.Request) string {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
