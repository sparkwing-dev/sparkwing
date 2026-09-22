package googleauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
)

const redirect = "https://dash.example/auth/google/callback"

var alice = googletest.Person{
	Subject: "g-alice", Email: "Alice@Example.com", EmailVerified: true, Name: "Alice A", GivenName: "Alice",
}

func TestExchangeReturnsVerifiedClaims(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	claims, err := c.Exchange(context.Background(), iss.Code(alice, "v1", redirect), "v1", redirect)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if claims.Subject != "g-alice" || claims.Email != "alice@example.com" || !claims.EmailVerified || claims.GivenName != "Alice" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestExchangeRefusesAWrongVerifier(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	_, err := c.Exchange(context.Background(), iss.Code(alice, "v1", redirect), "not-v1", redirect)
	if !errors.Is(err, googleauth.ErrRejected) {
		t.Fatalf("Exchange = %v, want ErrRejected", err)
	}
}

func TestExchangeRefusesTokensThatDoNotProveTheSignIn(t *testing.T) {
	iss := googletest.New(t)
	cases := map[string]map[string]any{
		"another audience": {"aud": "someone-else.apps.googleusercontent.com"},
		"another issuer":   {"iss": "https://evil.example"},
		"expired":          {"exp": time.Now().Add(-2 * time.Hour).Unix()},
		"no expiry":        {"exp": nil},
		"no subject":       {"sub": ""},
		"multi-aud no azp": {"aud": []string{iss.ClientID, "other"}},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			c := googleauth.New(iss.Config())
			_, err := c.Exchange(context.Background(), iss.CodeWith(alice, "v", redirect, override), "v", redirect)
			if !errors.Is(err, googleauth.ErrRejected) {
				t.Fatalf("Exchange = %v, want ErrRejected", err)
			}
		})
	}
}

func TestExchangeRefusesAnUnverifiedEmail(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	p := alice
	p.EmailVerified = false
	_, err := c.Exchange(context.Background(), iss.Code(p, "v", redirect), "v", redirect)
	if !errors.Is(err, googleauth.ErrUnverified) {
		t.Fatalf("Exchange = %v, want ErrUnverified", err)
	}
}

func TestExchangeAcceptsEmailVerifiedSpelledAsAString(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	code := iss.CodeWith(alice, "v", redirect, map[string]any{"email_verified": "true"})
	if _, err := c.Exchange(context.Background(), code, "v", redirect); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
}

func TestExchangeRefusesATokenSignedByAnUnpublishedKey(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exchange(context.Background(), iss.CodeSignedBy(rogue, alice, "v", redirect), "v", redirect)
	if !errors.Is(err, googleauth.ErrRejected) {
		t.Fatalf("Exchange = %v, want ErrRejected", err)
	}
}

func TestAuthorizeURLCarriesTheChallengeNotTheVerifier(t *testing.T) {
	c := googleauth.New(googleauth.Google("cid", "secret"))
	u, err := url.Parse(c.AuthorizeURL("st", "the-verifier", redirect))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge") != googleauth.CodeChallenge("the-verifier") || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("challenge = %q", q.Get("code_challenge"))
	}
	for k, v := range q {
		for _, s := range v {
			if s == "the-verifier" {
				t.Fatalf("authorize URL carries the verifier in %s", k)
			}
		}
	}
	if q.Get("state") != "st" || q.Get("redirect_uri") != redirect || q.Get("client_id") != "cid" {
		t.Fatalf("query = %v", q)
	}
}
