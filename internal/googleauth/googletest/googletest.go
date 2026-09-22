// Package googletest runs a local stand-in for Google's token and key
// endpoints, so a suite can drive a complete sign-in, including ID token
// verification, without reaching Google.
package googletest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
)

// Issuer is the fake. Its Issuer URL is what ID tokens name in iss.
type Issuer struct {
	URL          string
	ClientID     string
	ClientSecret string

	signer *jwkstest.Signer
	mu     sync.Mutex
	codes  map[string]grant
	srv    *httptest.Server
}

type grant struct {
	verifier    string
	redirectURI string
	claims      map[string]any
	signer      *rsa.PrivateKey
}

// Person is who a code signs in as.
type Person struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
}

// New starts an issuer and stops it when t ends.
func New(t testing.TB) *Issuer {
	t.Helper()
	iss := &Issuer{
		ClientID: "client-123.apps.googleusercontent.com", ClientSecret: "shh",
		signer: jwkstest.NewSigner(t, "test-key"), codes: map[string]grant{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", iss.handleToken)
	mux.HandleFunc("GET /certs", iss.signer.ServeKeys)
	iss.srv = httptest.NewServer(mux)
	iss.URL = iss.srv.URL
	t.Cleanup(iss.srv.Close)
	return iss
}

// Config returns a client configuration pointed at this issuer.
func (i *Issuer) Config() googleauth.Config {
	return googleauth.Config{
		ClientID: i.ClientID, ClientSecret: i.ClientSecret,
		AuthURL: i.URL + "/auth", TokenURL: i.URL + "/token", JWKSURL: i.URL + "/certs",
		Issuers: []string{i.URL},
	}
}

// Code issues an authorization code that redeems, with verifier and
// redirectURI, for an ID token about p.
func (i *Issuer) Code(p Person, verifier, redirectURI string) string {
	return i.CodeWith(p, verifier, redirectURI, nil)
}

// CodeWith is Code with claim overrides, which is how a test builds a token
// with the wrong audience, issuer or expiry. A nil value deletes the claim.
func (i *Issuer) CodeWith(p Person, verifier, redirectURI string, override map[string]any) string {
	return i.code(p, verifier, redirectURI, override, i.signer.Key)
}

// CodeSignedBy issues a code whose ID token is signed by a key the issuer does
// not publish.
func (i *Issuer) CodeSignedBy(signer *rsa.PrivateKey, p Person, verifier, redirectURI string) string {
	return i.code(p, verifier, redirectURI, nil, signer)
}

func (i *Issuer) code(p Person, verifier, redirectURI string, override map[string]any, signer *rsa.PrivateKey) string {
	now := time.Now()
	claims := map[string]any{
		"iss": i.URL, "aud": i.ClientID, "sub": p.Subject,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": p.Email, "email_verified": p.EmailVerified,
		"name": p.Name, "given_name": p.GivenName,
	}
	for k, v := range override {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	code := rand.Text()
	i.mu.Lock()
	i.codes[code] = grant{verifier: verifier, redirectURI: redirectURI, claims: claims, signer: signer}
	i.mu.Unlock()
	return code
}

func (i *Issuer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	refuse := func(reason string) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": reason})
	}
	if r.PostForm.Get("client_id") != i.ClientID || r.PostForm.Get("client_secret") != i.ClientSecret {
		refuse("invalid_client")
		return
	}
	code := r.PostForm.Get("code")
	i.mu.Lock()
	g, ok := i.codes[code]
	delete(i.codes, code)
	i.mu.Unlock()
	switch {
	case !ok:
		refuse("invalid_grant")
		return
	case r.PostForm.Get("code_verifier") != g.verifier:
		refuse("invalid_grant")
		return
	case r.PostForm.Get("redirect_uri") != g.redirectURI:
		refuse("redirect_uri_mismatch")
		return
	}
	token, err := i.signer.SignWith(g.signer, g.claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id_token": token})
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
