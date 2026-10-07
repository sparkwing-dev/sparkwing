package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a link flow is a few redirects long; a state that outlives that is
// one someone kept, not one a browser is using.
const identityLinkStateTTL = 10 * time.Minute

// safety: each attempt costs the provider a code exchange and a user read, so
// one account cannot drive a stream of them.
const identityLinkAttemptsPerMinute = 10

// safety: a session left open on a shared machine must not be able to add a
// way into the account or remove its owner's, so both ask for a fresh sign-in.
const identityChangeSignInWindow = 10 * time.Minute

var providerLabels = map[string]string{store.ProviderGoogle: "Google", store.ProviderGitHub: "GitHub"}

type identityJSON struct {
	Provider  string `json:"provider"`
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

type identitiesResp struct {
	Identities []identityJSON `json:"identities"`
	Providers  []string       `json:"providers"`
}

func identityOut(id store.Identity) identityJSON {
	return identityJSON{Provider: id.Provider, Email: id.Email, CreatedAt: id.CreatedAt.Unix()}
}

func (s *Server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	ids, err := s.store.AccountIdentities(r.Context(), p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "list identities", err)
		return
	}
	out := identitiesResp{Identities: []identityJSON{}, Providers: s.signInProviders()}
	for _, id := range ids {
		out.Identities = append(out.Identities, identityOut(id))
	}
	writeJSON(w, http.StatusOK, out)
}

type identityLinkState struct {
	Account  string `json:"a"`
	Session  string `json:"s"`
	Provider string `json:"p"`
	Expires  int64  `json:"e"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// safety: the state travels through the provider's URLs, so it names the
// session by a MAC only this controller can compute, never by its id.
func sessionBinding(key []byte, rawSession string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("session\x00" + rawSession))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) identityChangeRefusal(p *Principal, status int, code, provider, message string) *flowRefusal {
	s.logger.Info("identity.change_refused", "account", p.AccountID, "provider", provider, "reason", code)
	return refuse(status, code, message)
}

func (s *Server) refuseIdentityChange(w http.ResponseWriter, p *Principal, refusal *flowRefusal) {
	writeAuthError(w, refusal.status, authErrorBody{Code: refusal.code, Principal: p.label(), Message: refusal.message})
}

func (s *Server) recentSignIn(p *Principal, provider string) *flowRefusal {
	if time.Since(p.signedInAt) <= identityChangeSignInWindow {
		return nil
	}
	return s.identityChangeRefusal(p, http.StatusForbidden, "reauth_required", provider,
		"linking or unlinking a sign-in needs a sign-in from the last 10 minutes; sign out, sign in again and retry")
}

func (s *Server) linkAttemptAllowed(p *Principal, provider string) *flowRefusal {
	if s.identityLinkLimit.allow(p.AccountID, identityLinkAttemptsPerMinute, time.Now()) {
		return nil
	}
	return s.identityChangeRefusal(p, http.StatusTooManyRequests, "rate_limited", provider,
		"too many link attempts; wait a minute and try again")
}

func (s *Server) linkPreconditions(p *Principal, name string) (signInProvider, error) {
	provider, err := s.offeredProvider(name)
	if err != nil {
		return nil, err
	}
	if refusal := s.linkAttemptAllowed(p, name); refusal != nil {
		return nil, refusal
	}
	if refusal := s.recentSignIn(p, name); refusal != nil {
		return nil, refusal
	}
	return provider, nil
}

func (s *Server) identityLinkBegin(ctx context.Context, p *Principal, name, redirectURI string) (oauthGrant, error) {
	provider, err := s.linkPreconditions(p, name)
	if err != nil {
		return oauthGrant{}, err
	}
	if !s.redirectAllowed(redirectURI) {
		return oauthGrant{}, refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	ids, err := s.store.AccountIdentities(ctx, p.AccountID)
	if err != nil {
		return oauthGrant{}, fmt.Errorf("link start: %w", err)
	}
	if slices.ContainsFunc(ids, func(id store.Identity) bool { return id.Provider == name }) {
		return oauthGrant{}, s.identityChangeRefusal(p, http.StatusConflict, "provider_already_linked", name,
			"this account already has a "+providerLabels[name]+" sign-in; unlink it before linking another")
	}
	key, err := s.store.IdentityLinkStateKey(ctx)
	if err != nil {
		return oauthGrant{}, fmt.Errorf("link start: %w", err)
	}
	nonce, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, err
	}
	verifier, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, err
	}
	state, err := signFlowState(key, identityLinkState{
		Account: p.AccountID, Session: sessionBinding(key, p.session), Provider: name,
		Expires: time.Now().Add(identityLinkStateTTL).Unix(), Nonce: nonce, Verifier: verifierDigest(verifier),
	})
	if err != nil {
		return oauthGrant{}, fmt.Errorf("link start: %w", err)
	}
	return oauthGrant{AuthorizeURL: provider.AuthorizeURL(state, verifier, redirectURI), State: state, Verifier: verifier}, nil
}

func openLinkState(key []byte, raw, verifier, provider string, p *Principal, now time.Time) (identityLinkState, string) {
	var st identityLinkState
	switch {
	case !openFlowState(key, raw, &st):
		return st, "this link could not be verified; start linking again"
	case now.Unix() >= st.Expires:
		return st, "this link expired; start linking again"
	case st.Account != p.AccountID || !hmac.Equal([]byte(st.Session), []byte(sessionBinding(key, p.session))):
		return st, "this link was started by another account or in another session; start linking again"
	case st.Provider != provider:
		return st, "this link was started for another provider; start linking again"
	case verifier == "" || !hmac.Equal([]byte(st.Verifier), []byte(verifierDigest(verifier))):
		return st, "this link was started in another browser; start linking again"
	}
	return st, ""
}

func (s *Server) identityLinkFinish(ctx context.Context, p *Principal, name string, flow oauthFlowProof, redirectURI string) error {
	provider, err := s.linkPreconditions(p, name)
	if err != nil {
		return err
	}
	if flow.Code == "" {
		return refuse(http.StatusBadRequest, "", "code is required")
	}
	if !s.redirectAllowed(redirectURI) {
		return refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	key, err := s.store.IdentityLinkStateKey(ctx)
	if err != nil {
		return fmt.Errorf("link complete: %w", err)
	}
	now := time.Now()
	st, reason := openLinkState(key, flow.State, flow.Verifier, name, p, now)
	if reason == "" {
		// safety: the used state is recorded in the store, so no replica and no
		// restart finishes the same flow twice.
		fresh, err := s.store.ConsumeIdentityLinkState(ctx, st.Nonce, time.Unix(st.Expires, 0), now)
		if err != nil {
			return fmt.Errorf("link state: %w", err)
		}
		if !fresh {
			reason = "this link was already used; start linking again"
		}
	}
	if reason != "" {
		return s.identityChangeRefusal(p, http.StatusForbidden, "link_state_invalid", name, reason)
	}
	label := providerLabels[name]
	profile, err := provider.SignIn(ctx, flow.Code, flow.Verifier, redirectURI)
	switch {
	case errors.Is(err, errSignInUnverified):
		return s.identityChangeRefusal(p, http.StatusForbidden, "provider_unverified", name,
			label+" has not verified this account's email address")
	case errors.Is(err, errSignInRejected):
		return s.identityChangeRefusal(p, http.StatusUnauthorized, "provider_rejected", name,
			label+" did not confirm the sign-in; start linking again")
	case err != nil:
		s.logger.Warn("identity link failed", "provider", name, "error", err.Error())
		return refuse(http.StatusBadGateway, "provider_unreachable", label+" could not be reached to finish linking")
	}
	if refusal := s.recentSignIn(p, name); refusal != nil {
		return refusal
	}
	linked, err := s.store.LinkIdentity(ctx, p.AccountID, profile, now)
	switch {
	case errors.Is(err, store.ErrIdentityLinkedElsewhere):
		return s.identityChangeRefusal(p, http.StatusConflict, "identity_linked_elsewhere", name,
			"that "+label+" sign-in is already linked to another Sparkwing account, so nothing changed. "+
				"Sparkwing does not combine accounts: invite one account into the other's team, "+
				"or delete the other account and then link its sign-in here")
	case errors.Is(err, store.ErrIdentityAlreadyLinked):
		return s.identityChangeRefusal(p, http.StatusConflict, "identity_already_linked", name,
			"that "+label+" sign-in is already linked to this account")
	case errors.Is(err, store.ErrProviderAlreadyLinked):
		return s.identityChangeRefusal(p, http.StatusConflict, "provider_already_linked", name,
			"this account already has a "+label+" sign-in; unlink it before linking another")
	case err != nil:
		return fmt.Errorf("link identity: %w", err)
	}
	s.logger.Info("identity.linked", "account", p.AccountID, "provider", name, "subject", linked.Subject)
	return nil
}

func (s *Server) handleIdentityUnlink(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		writeError(w, http.StatusNotFound, errors.New("no sign-in provider by that name"))
		return
	}
	if refusal := s.recentSignIn(p, name); refusal != nil {
		s.refuseIdentityChange(w, p, refusal)
		return
	}
	gone, ended, err := s.store.UnlinkIdentity(r.Context(), p.AccountID, name, p.session, time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("this account has no "+providerLabels[name]+" sign-in"))
		return
	case errors.Is(err, store.ErrLastSignInMethod):
		s.refuseIdentityChange(w, p, s.identityChangeRefusal(p, http.StatusConflict, "last_sign_in_method", name,
			"this is the account's only sign-in method; link another before unlinking it"))
		return
	case err != nil:
		s.writeInternalError(w, r, "unlink identity", err)
		return
	}
	s.logger.Info("identity.unlinked", "account", p.AccountID, "provider", name, "subject", gone.Subject,
		"sessions_ended", ended)
	w.WriteHeader(http.StatusNoContent)
}
