package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type oauthProofKey struct{}

type oauthProof struct {
	flow    oauthFlow
	grant   oauthGrant
	session *store.Session
	raw     string
}

func (s *Server) oauthEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/auth/{provider}/start", Authentication: capability.AuthBrowserFlow, Verify: s.verifyOAuthStart, Handle: s.handleOAuthStart},
		{Method: "GET", Pattern: "/auth/{provider}/callback", Authentication: capability.AuthBrowserFlow, Verify: s.verifyOAuthCallback, Handle: s.handleOAuthCallback},
	}
}

func (s *Server) verifyOAuthStart(r *http.Request) error {
	if err := s.loginLimit.admit(r); err != nil {
		return err
	}
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		return &apierr.Error{Code: apierr.NotFound, Message: "sign-in provider is unavailable"}
	}
	grant, err := s.oauthBegin(name, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		return oauthFlowError(err)
	}
	proof := oauthProof{grant: grant, flow: oauthFlow{Provider: name, State: grant.State, Verifier: grant.Verifier, Next: safeNext(r.URL.Query().Get("next"))}}
	*r = *r.WithContext(context.WithValue(r.Context(), oauthProofKey{}, proof))
	return nil
}

func (s *Server) verifyOAuthCallback(r *http.Request) error {
	if err := s.loginLimit.admit(r); err != nil {
		return err
	}
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		return &apierr.Error{Code: apierr.NotFound, Message: "sign-in provider is unavailable"}
	}
	if _, err := s.offeredProvider(name); err != nil {
		return oauthFlowError(err)
	}
	if !s.redirectAllowed(browserURL(r, oauthCallbackPath(name))) {
		return invalidEngineBody("redirect_uri is not on this controller's allowlist")
	}
	flow, started := s.readOAuthFlow(r)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if !started || err != nil || flow.Provider != name || flow.Code != "" || len(query["state"]) != 1 || !constantTimeEqual(query.Get("state"), flow.State) {
		return invalidEngineBody("this sign-in could not be verified; start again")
	}
	if values, declined := query["error"]; declined {
		if len(values) != 1 || values[0] == "" || len(query["code"]) != 0 {
			return invalidEngineBody("invalid sign-in callback")
		}
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "sign-in was not completed"}
	}
	if len(query["code"]) != 1 || query.Get("code") == "" {
		return invalidEngineBody("sign-in code is required")
	}
	proof := oauthProof{flow: flow}
	if flow.Mode == oauthFlowLink {
		proof.flow.Code = query.Get("code")
	} else {
		if s.operator == nil {
			return missingEndpointPolicy()
		}
		proof.session, proof.raw, err = s.oauthSignIn(r.Context(), name, query.Get("code"), flow.Verifier, browserURL(r, oauthCallbackPath(name)))
		if err != nil {
			return oauthFlowError(err)
		}
		notePolicyPrincipal(r.Context(), capability.Principal{Class: capability.CredentialSession, Capability: capability.Capability{
			Team: string(proof.session.Team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: proof.session.AccountID},
		}})
	}
	*r = *r.WithContext(context.WithValue(r.Context(), oauthProofKey{}, proof))
	return nil
}

func oauthFlowError(err error) error {
	if errors.Is(err, store.ErrNotMember) {
		return apierr.Authorization(apierr.ReasonNoTeam, "account has no active team membership")
	}
	if refused, ok := refusalOf(err); ok {
		codes := map[int]apierr.Code{400: apierr.BadRequest, 401: apierr.Unauthenticated, 403: apierr.Forbidden, 404: apierr.NotFound, 409: apierr.Conflict}
		if code, ok := codes[refused.status]; ok {
			return &apierr.Error{Code: code, Message: refused.message}
		}
	}
	return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "sign-in service unavailable; start again"}, err)
}
