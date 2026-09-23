// Package githubtest runs a local stand-in for GitHub's token, user and email
// endpoints, so a suite can drive a complete GitHub sign-in without GitHub.
package githubtest

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
)

// Email is one address on a fake account.
type Email struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// Person is who a code signs in as. ProfileEmail is the public address on the
// profile, which a correct client never trusts. CreatedAt is when the account
// was opened; zero reports an account opened a year ago, so a suite that is
// not about account age signs in an established account.
type Person struct {
	ID           int64
	Login        string
	Name         string
	ProfileEmail string
	Emails       []Email
	CreatedAt    time.Time
}

// Server is the fake.
type Server struct {
	URL          string
	ClientID     string
	ClientSecret string

	mu     sync.Mutex
	codes  map[string]grant
	tokens map[string]Person
}

type grant struct {
	verifier, redirectURI string
	person                Person
}

// New starts a server and stops it when t ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{ClientID: "gh-client", ClientSecret: "gh-secret", codes: map[string]grant{}, tokens: map[string]Person{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", s.handleToken)
	mux.HandleFunc("GET /user", s.handleUser)
	mux.HandleFunc("GET /user/emails", s.handleEmails)
	srv := httptest.NewServer(mux)
	s.URL = srv.URL
	t.Cleanup(srv.Close)
	return s
}

// Config returns a client configuration pointed at this server.
func (s *Server) Config() githubauth.Config {
	return githubauth.Config{
		ClientID: s.ClientID, ClientSecret: s.ClientSecret,
		AuthURL: s.URL + "/login/oauth/authorize", TokenURL: s.URL + "/login/oauth/access_token",
		UserURL: s.URL + "/user", EmailsURL: s.URL + "/user/emails",
	}
}

// Code issues an authorization code that redeems, with verifier and
// redirectURI, for an access token belonging to p.
func (s *Server) Code(p Person, verifier, redirectURI string) string {
	code := rand.Text()
	s.mu.Lock()
	s.codes[code] = grant{verifier: verifier, redirectURI: redirectURI, person: p}
	s.mu.Unlock()
	return code
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("client_id") != s.ClientID || r.PostForm.Get("client_secret") != s.ClientSecret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	g, ok := s.codes[code]
	delete(s.codes, code)
	s.mu.Unlock()
	if !ok || r.PostForm.Get("code_verifier") != g.verifier || r.PostForm.Get("redirect_uri") != g.redirectURI {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	token := rand.Text()
	s.mu.Lock()
	s.tokens[token] = g.person
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer"})
}

func (s *Server) person(r *http.Request) (Person, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	return p, ok
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.person(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = time.Now().AddDate(-1, 0, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": p.ID, "login": p.Login, "name": p.Name, "email": p.ProfileEmail,
		"created_at": created.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleEmails(w http.ResponseWriter, r *http.Request) {
	p, ok := s.person(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	emails := p.Emails
	if emails == nil {
		emails = []Email{}
	}
	writeJSON(w, http.StatusOK, emails)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		return
	}
}
