package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var (
	errSignInRejected   = errors.New("sign-in rejected")
	errSignInUnverified = errors.New("the provider has not verified the email address")
)

type signInProvider interface {
	AuthorizeURL(state, verifier, redirectURI string) string
	SignIn(ctx context.Context, code, verifier, redirectURI string) (store.SignInProfile, error)
}

type googleProvider struct{ c *googleauth.Client }

func (g googleProvider) AuthorizeURL(state, verifier, redirectURI string) string {
	return g.c.AuthorizeURL(state, verifier, redirectURI)
}

func (g googleProvider) SignIn(ctx context.Context, code, verifier, redirectURI string) (store.SignInProfile, error) {
	claims, err := g.c.Exchange(ctx, code, verifier, redirectURI)
	switch {
	case errors.Is(err, googleauth.ErrUnverified):
		return store.SignInProfile{}, fmt.Errorf("%w: %w", errSignInUnverified, err)
	case errors.Is(err, googleauth.ErrRejected):
		return store.SignInProfile{}, fmt.Errorf("%w: %w", errSignInRejected, err)
	case err != nil:
		return store.SignInProfile{}, err
	}
	return store.SignInProfile{
		Provider: store.ProviderGoogle, Subject: claims.Subject, Email: claims.Email,
		EmailVerified: claims.EmailVerified, Name: claims.Name, GivenName: claims.GivenName,
	}, nil
}

type githubProvider struct{ c *githubauth.Client }

func (g githubProvider) AuthorizeURL(state, verifier, redirectURI string) string {
	return g.c.AuthorizeURL(state, verifier, redirectURI)
}

func (g githubProvider) SignIn(ctx context.Context, code, verifier, redirectURI string) (store.SignInProfile, error) {
	p, err := g.c.Exchange(ctx, code, verifier, redirectURI)
	switch {
	case errors.Is(err, githubauth.ErrUnverified):
		return store.SignInProfile{}, fmt.Errorf("%w: %w", errSignInUnverified, err)
	case errors.Is(err, githubauth.ErrRejected):
		return store.SignInProfile{}, fmt.Errorf("%w: %w", errSignInRejected, err)
	case err != nil:
		return store.SignInProfile{}, err
	}
	given := p.Name
	if given == "" {
		given = p.Login
	}
	return store.SignInProfile{
		Provider: store.ProviderGitHub, Subject: p.Subject, Email: p.Email,
		EmailVerified: true, Name: p.Name, GivenName: firstWord(given),
		ProviderAccountCreatedAt: p.CreatedAt,
	}, nil
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}
