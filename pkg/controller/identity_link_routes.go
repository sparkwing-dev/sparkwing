package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// IdentityLinkWriter rechecks the direct session and commits nonce consumption and identity attachment.
type IdentityLinkWriter interface {
	ConsumeIdentityLink(context.Context, capability.IdentityLink) error
	LinkAccountIdentity(context.Context, capability.IdentityLink, store.SignInProfile) (store.Identity, error)
}

// WithIdentityLinkWriter supplies account-link transactions before Handler is constructed.
func (s *Server) WithIdentityLinkWriter(writer IdentityLinkWriter) *Server {
	s.identityLinkWrites = writer
	return s
}

func (s *Server) identityLinkWriter() (IdentityLinkWriter, error) {
	if s.identityLinkWrites != nil {
		return s.identityLinkWrites, nil
	}
	if writer, ok := any(s.store).(IdentityLinkWriter); ok && s.store != nil {
		return writer, nil
	}
	return nil, missingEndpointPolicy()
}

type identityLinkProofKey struct{}

type identityLinkProof struct{ grant oauthGrant }

func (s *Server) identityLinkEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/auth/{provider}/link", Authentication: capability.AuthBrowserFlow, Verify: s.verifyIdentityLinkStart, Handle: s.handleIdentityLinkStart},
		{Method: "GET", Pattern: "/auth/{provider}/link/complete", Authentication: capability.AuthBrowserFlow, Verify: s.verifyIdentityLinkComplete, Handle: s.handleIdentityLinkComplete},
	}
}

func (s *Server) identityLinkSession(r *http.Request) (*store.Session, signInProvider, error) {
	if len(r.Header.Values("Authorization")) != 0 {
		return nil, nil, sessionUnavailable()
	}
	session, err := s.readSessionFlow(r)
	if err != nil {
		return nil, nil, err
	}
	if session.CapabilityPrefix != "" {
		return nil, nil, identityLinkRefusal(apierr.Forbidden, "direct_sign_in_required", "linking requires the account's own provider sign-in")
	}
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		return nil, nil, &apierr.Error{Code: apierr.NotFound, Message: "sign-in provider is unavailable"}
	}
	provider, err := s.offeredProvider(name)
	if err != nil {
		return nil, nil, oauthFlowError(err)
	}
	if !s.redirectAllowed(browserURL(r, oauthCallbackPath(name))) {
		return nil, nil, invalidEngineBody("redirect_uri is not on this controller's allowlist")
	}
	now := time.Now().UTC()
	if !recentSignIn(session.CreatedAt, now) {
		return nil, nil, identityLinkRefusal(apierr.Forbidden, "reauth_required", "linking needs a sign-in from the last 10 minutes; sign out, sign in again and retry")
	}
	notePolicyPrincipal(r.Context(), capability.Principal{Class: capability.CredentialSession, Capability: capability.Capability{
		Team: string(session.Team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: session.AccountID},
	}})
	return session, provider, nil
}

func (s *Server) admitIdentityLink(session *store.Session) error {
	if !s.identityLinkLimit.allow(session.AccountID, identityLinkAttemptsPerMinute, time.Now()) {
		return &apierr.Error{Code: apierr.RateLimited, Message: "too many link attempts; wait a minute and try again", Details: map[string]any{"retry_after_ms": time.Minute.Milliseconds()}}
	}
	return nil
}

func (s *Server) verifyIdentityLinkStart(r *http.Request) error {
	session, provider, err := s.identityLinkSession(r)
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		return invalidEngineBody("invalid linking form")
	}
	if len(r.PostForm) != 1 || len(r.PostForm["csrf_token"]) != 1 || len(r.CookiesNamed(cookieName(csrfCookieName, s.cookiesSecure()))) != 1 ||
		!validFormCSRF(r, s.cookiesSecure()) || !constantTimeEqual(r.PostForm.Get("csrf_token"), session.CSRFToken) {
		return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
	}
	if err := s.admitIdentityLink(session); err != nil {
		return err
	}
	if _, err := s.identityLinkWriter(); err != nil {
		return err
	}
	reader, err := s.accountReader()
	if err != nil {
		return err
	}
	ids, err := reader.AccountIdentities(r.Context(), session.AccountID)
	if err != nil {
		return identityLinkError(err)
	}
	name := r.PathValue("provider")
	if slices.ContainsFunc(ids, func(id store.Identity) bool { return id.Provider == name }) {
		return identityLinkError(store.ErrProviderAlreadyLinked)
	}
	grant, err := s.newIdentityLink(r.Context(), session, name, provider, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), identityLinkProofKey{}, identityLinkProof{grant: grant}))
	return nil
}

func (s *Server) newIdentityLink(ctx context.Context, session *store.Session, name string, provider signInProvider, redirect string) (oauthGrant, error) {
	key, err := s.store.IdentityLinkStateKey(ctx)
	if err != nil {
		return oauthGrant{}, identityLinkError(err)
	}
	nonce, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, identityLinkError(err)
	}
	verifier, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, identityLinkError(err)
	}
	state, err := signFlowState(key, identityLinkState{
		Account: session.AccountID, Session: sessionBinding(key, session.ID), Provider: name,
		Expires: time.Now().Add(identityLinkStateTTL).Unix(), Nonce: nonce, Verifier: verifierDigest(verifier),
	})
	if err != nil {
		return oauthGrant{}, identityLinkError(err)
	}
	return oauthGrant{AuthorizeURL: provider.AuthorizeURL(state, verifier, redirect), State: state, Verifier: verifier}, nil
}

func (s *Server) verifyIdentityLinkComplete(r *http.Request) error {
	session, provider, err := s.identityLinkSession(r)
	if err != nil {
		return err
	}
	flow, ok := s.readOAuthFlow(r)
	if !ok || flow.Mode != oauthFlowLink || flow.Provider != r.PathValue("provider") || flow.Code == "" {
		return invalidIdentityLink()
	}
	key, err := s.store.IdentityLinkStateKey(r.Context())
	if err != nil {
		return identityLinkError(err)
	}
	var state identityLinkState
	if !openFlowState(key, flow.State, &state) || state.Account != session.AccountID || state.Provider != flow.Provider || state.Nonce == "" ||
		time.Now().Unix() >= state.Expires || !constantTimeEqual(state.Session, sessionBinding(key, session.ID)) || !constantTimeEqual(state.Verifier, verifierDigest(flow.Verifier)) {
		return invalidIdentityLink()
	}
	if err := s.admitIdentityLink(session); err != nil {
		return err
	}
	writer, err := s.identityLinkWriter()
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(session.ID))
	claim := capability.IdentityLink{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, AccountID: state.Account, Provider: state.Provider, Nonce: state.Nonce, ExpiresAt: time.Unix(state.Expires, 0)}
	if err := writer.ConsumeIdentityLink(r.Context(), claim); err != nil {
		return identityLinkError(err)
	}
	profile, err := provider.SignIn(r.Context(), flow.Code, flow.Verifier, browserURL(r, oauthCallbackPath(flow.Provider)))
	if err != nil {
		return identityLinkProviderError(err)
	}
	if profile.Provider != claim.Provider || profile.Subject == "" || !profile.EmailVerified {
		return identityLinkRefusal(apierr.Forbidden, "provider_unverified", "the provider did not verify this sign-in")
	}
	if _, err := writer.LinkAccountIdentity(r.Context(), claim, profile); err != nil {
		return identityLinkError(err)
	}
	*r = *r.WithContext(context.WithValue(r.Context(), identityLinkProofKey{}, identityLinkProof{}))
	return nil
}

func identityLinkRefusal(code apierr.Code, reason, message string) error {
	return &apierr.Error{Code: code, Message: message, Details: map[string]any{"reason": reason}}
}

func invalidIdentityLink() error {
	return identityLinkRefusal(apierr.Forbidden, "link_state_invalid", "this link could not be verified; start linking again")
}

func identityLinkError(err error) error {
	var failure *apierr.Error
	if errors.As(err, &failure) {
		return err
	}
	switch {
	case errors.Is(err, store.ErrIdentityLinkedElsewhere):
		return identityLinkRefusal(apierr.Conflict, "identity_linked_elsewhere", "this sign-in belongs to another account; accounts cannot be combined")
	case errors.Is(err, store.ErrIdentityAlreadyLinked):
		return identityLinkRefusal(apierr.Conflict, "identity_already_linked", "this sign-in already belongs to this account")
	case errors.Is(err, store.ErrProviderAlreadyLinked):
		return identityLinkRefusal(apierr.Conflict, "provider_already_linked", "this account already has a sign-in from this provider; unlink it before linking another")
	default:
		return &apierr.Error{Code: apierr.Unavailable, Message: "linking service unavailable; start linking again"}
	}
}

func identityLinkProviderError(err error) error {
	switch {
	case errors.Is(err, errSignInRejected):
		return identityLinkRefusal(apierr.Unauthenticated, "provider_rejected", "the provider did not confirm the sign-in; start linking again")
	case errors.Is(err, errSignInUnverified):
		return identityLinkRefusal(apierr.Forbidden, "provider_unverified", "the provider has not verified this account's email address")
	default:
		return &apierr.Error{Code: apierr.Unavailable, Message: "linking service unavailable; start linking again"}
	}
}

func (s *Server) handleIdentityLinkStart(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(identityLinkProofKey{}).(identityLinkProof)
	if !ok || proof.grant.AuthorizeURL == "" {
		return missingEndpointPolicy()
	}
	grant, label := proof.grant, providerLabels[r.PathValue("provider")]
	if !setFlowCookie(w, oauthFlowCookieName, oauthFlow{Provider: r.PathValue("provider"), State: grant.State, Verifier: grant.Verifier, Mode: oauthFlowLink}, s.cookiesSecure()) {
		return &apierr.Error{Code: apierr.Internal, Message: "could not start linking"}
	}
	renderFlowPage(w, http.StatusOK, flowPage{Title: "Linking " + label, Message: "Continuing to " + label + " to confirm the account.", Refresh: grant.AuthorizeURL, ActionHref: grant.AuthorizeURL, ActionLabel: "Continue to " + label})
	return nil
}

func (s *Server) handleIdentityLinkComplete(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if _, ok := r.Context().Value(identityLinkProofKey{}).(identityLinkProof); !ok {
		return missingEndpointPolicy()
	}
	clearFlowCookie(w, oauthFlowCookieName, s.cookiesSecure())
	noteAuditWrite(r.Context())
	http.Redirect(w, r, "/account/sign-ins?"+url.Values{"linked": {r.PathValue("provider")}}.Encode(), http.StatusSeeOther)
	return nil
}
