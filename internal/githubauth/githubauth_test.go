package githubauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
)

const redirect = "http://localhost:4343/auth/github/callback"

func TestExchangeReadsTheNumericIDAndPrimaryVerifiedEmail(t *testing.T) {
	gh := githubtest.New(t)
	c := githubauth.New(gh.Config())
	p, err := c.Exchange(context.Background(), gh.Code(githubtest.Person{ID: 42, Login: "octo", Emails: []githubtest.Email{
		{Email: "old@example.com", Primary: false, Verified: true},
		{Email: "Octo@Example.com", Primary: true, Verified: true},
	}}, "v", redirect), "v", redirect)
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "42" || p.Email != "octo@example.com" || p.Login != "octo" {
		t.Fatalf("profile = %+v", p)
	}
}

func TestExchangeRefusesAWrongVerifier(t *testing.T) {
	gh := githubtest.New(t)
	c := githubauth.New(gh.Config())
	_, err := c.Exchange(context.Background(), gh.Code(githubtest.Person{ID: 1, Login: "x"}, "v", redirect), "other", redirect)
	if !errors.Is(err, githubauth.ErrRejected) {
		t.Fatalf("Exchange = %v, want ErrRejected", err)
	}
}

func TestExchangeRefusesAnAccountWithoutAPrimaryVerifiedEmail(t *testing.T) {
	gh := githubtest.New(t)
	c := githubauth.New(gh.Config())
	_, err := c.Exchange(context.Background(), gh.Code(githubtest.Person{ID: 1, Login: "x", ProfileEmail: "x@example.com"},
		"v", redirect), "v", redirect)
	if !errors.Is(err, githubauth.ErrUnverified) {
		t.Fatalf("Exchange = %v, want ErrUnverified", err)
	}
}
