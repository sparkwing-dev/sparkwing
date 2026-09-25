package googleauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/url"
	"strings"
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
		"issued in future": {"iat": time.Now().Add(2 * time.Hour).Unix()},
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

func TestExchangeRefusesATokenWithoutAnEmail(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	code := iss.CodeWith(alice, "v", redirect, map[string]any{"email": nil})
	if _, err := c.Exchange(context.Background(), code, "v", redirect); !errors.Is(err, googleauth.ErrUnverified) {
		t.Fatalf("Exchange = %v, want ErrUnverified", err)
	}
}

func TestExchangeAcceptsSeveralAudiencesWhenThisClientIsTheAuthorizedParty(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	code := iss.CodeWith(alice, "v", redirect, map[string]any{"aud": []string{"other", iss.ClientID}, "azp": iss.ClientID})
	if _, err := c.Exchange(context.Background(), code, "v", redirect); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
}

func TestExchangeFailsWhenTheTokenEndpointIsUnreachable(t *testing.T) {
	iss := googletest.New(t)
	cfg := iss.Config()
	cfg.HTTP = &http.Client{Transport: unreachable{}}
	if _, err := googleauth.New(cfg).Exchange(context.Background(), "code", "v", redirect); err == nil {
		t.Fatal("Exchange against an unreachable endpoint succeeded")
	}
}

type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestVerifyRefusesATokenThatIsNotThreeSegments(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	signed := iss.IDToken(t, alice, nil, nil)
	unsigned := signed[:strings.LastIndex(signed, ".")]
	for _, token := range []string{"", unsigned, signed + ".extra"} {
		if _, err := c.Verify(context.Background(), token); !errors.Is(err, googleauth.ErrRejected) {
			t.Errorf("Verify(%q) = %v, want ErrRejected", token, err)
		}
	}
}

func TestVerifyRefusesAHeaderNamingAnotherAlgorithmEvenWhenRSASigned(t *testing.T) {
	iss := googletest.New(t)
	c := googleauth.New(iss.Config())
	for _, alg := range []string{"none", "HS256", "RS512"} {
		token := iss.IDToken(t, alice, nil, map[string]any{"alg": alg})
		if _, err := c.Verify(context.Background(), token); !errors.Is(err, googleauth.ErrRejected) {
			t.Errorf("Verify(alg=%s) = %v, want ErrRejected", alg, err)
		}
	}
}

// clock lets a test move the client's notion of now without sleeping.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestVerifyReusesKeysUntilTheirMaxAgeThenRefetches(t *testing.T) {
	iss := googletest.New(t)
	clk := &clock{now: time.Now()}
	cfg := iss.Config()
	cfg.Now = clk.Now
	c := googleauth.New(cfg)
	token := iss.IDToken(t, alice, map[string]any{"exp": clk.now.Add(3 * time.Hour).Unix()}, nil)
	for _, step := range []struct {
		advance time.Duration
		fetches int
	}{{0, 1}, {30 * time.Minute, 1}, {31 * time.Minute, 2}} {
		clk.now = clk.now.Add(step.advance)
		if _, err := c.Verify(context.Background(), token); err != nil {
			t.Fatalf("Verify after %v: %v", step.advance, err)
		}
		if got := iss.KeyFetches(); got != step.fetches {
			t.Fatalf("after %v the client fetched keys %d times, want %d", step.advance, got, step.fetches)
		}
	}
}

func TestVerifyRefetchesForAnUnknownKeyIDAtMostOncePerMinute(t *testing.T) {
	iss := googletest.New(t)
	clk := &clock{now: time.Now()}
	cfg := iss.Config()
	cfg.Now = clk.Now
	c := googleauth.New(cfg)
	if _, err := c.Verify(context.Background(), iss.IDToken(t, alice, nil, nil)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	forged := iss.IDToken(t, alice, nil, map[string]any{"kid": "forged"})
	for _, step := range []struct {
		advance time.Duration
		fetches int
	}{{0, 1}, {59 * time.Second, 1}, {time.Second, 2}} {
		clk.now = clk.now.Add(step.advance)
		if _, err := c.Verify(context.Background(), forged); !errors.Is(err, googleauth.ErrRejected) {
			t.Fatalf("Verify(unknown kid) after %v = %v, want ErrRejected", step.advance, err)
		}
		if got := iss.KeyFetches(); got != step.fetches {
			t.Fatalf("after %v the client fetched keys %d times, want %d", step.advance, got, step.fetches)
		}
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
