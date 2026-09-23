package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	// Providers are the sign-in providers this controller offers, which are
	// the ones an account can link.
	Providers []string `json:"providers"`
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

func (s *Server) refuseIdentityChange(w http.ResponseWriter, p *Principal, status int, code, provider, message string) {
	s.logger.Info("identity.change_refused", "account", p.AccountID, "provider", provider, "reason", code)
	writeAuthError(w, status, authErrorBody{Code: code, Principal: p.label(), Message: message})
}

func (s *Server) recentSignIn(w http.ResponseWriter, p *Principal, provider string) bool {
	if time.Since(p.signedInAt) <= identityChangeSignInWindow {
		return true
	}
	s.refuseIdentityChange(w, p, http.StatusForbidden, "reauth_required", provider,
		"linking or unlinking a sign-in needs a sign-in from the last 10 minutes; sign out, sign in again and retry")
	return false
}

func (s *Server) linkAttemptAllowed(w http.ResponseWriter, p *Principal, provider string) bool {
	if s.identityLinkLimit.allow(p.AccountID, identityLinkAttemptsPerMinute, time.Now()) {
		return true
	}
	setRetryAfter(w, time.Minute)
	s.refuseIdentityChange(w, p, http.StatusTooManyRequests, "rate_limited", provider,
		"too many link attempts; wait a minute and try again")
	return false
}

// handleIdentityLinkStart begins adding a provider's sign-in to the caller's
// account. The flow runs like sign-in, with a state this controller signs and
// binds to the account and the session that asked.
func (s *Server) handleIdentityLinkStart(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	name := r.PathValue("provider")
	provider, ok := s.offered(w, name)
	if !ok || !s.linkAttemptAllowed(w, p, name) || !s.recentSignIn(w, p, name) {
		return
	}
	var req oauthStartReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.redirectAllowed(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, errors.New("redirect_uri is not on this controller's allowlist"))
		return
	}
	ids, err := s.store.AccountIdentities(r.Context(), p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "link start", err)
		return
	}
	if slices.ContainsFunc(ids, func(id store.Identity) bool { return id.Provider == name }) {
		s.refuseIdentityChange(w, p, http.StatusConflict, "provider_already_linked", name,
			"this account already has a "+providerLabels[name]+" sign-in; unlink it before linking another")
		return
	}
	key, err := s.store.IdentityLinkStateKey()
	if err != nil {
		s.writeInternalError(w, r, "link start", err)
		return
	}
	nonce, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "link start", err)
		return
	}
	verifier, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "link start", err)
		return
	}
	state, err := signFlowState(key, identityLinkState{
		Account: p.AccountID, Session: sessionBinding(key, p.session), Provider: name,
		Expires: time.Now().Add(identityLinkStateTTL).Unix(), Nonce: nonce, Verifier: verifierDigest(verifier),
	})
	if err != nil {
		s.writeInternalError(w, r, "link start", err)
		return
	}
	writeJSON(w, http.StatusOK, oauthStartResp{
		AuthorizeURL: provider.AuthorizeURL(state, verifier, req.RedirectURI),
		State:        state,
		Verifier:     verifier,
	})
}

type identityLinkCompleteReq struct {
	State       string `json:"state"`
	Verifier    string `json:"verifier"`
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// openLinkState reports why a state cannot finish a link for p, or "" when it
// can.
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

// handleIdentityLinkComplete finishes a link: it redeems the provider's code
// and attaches the provider account to the caller's account, keyed by the
// provider's stable subject. A provider account attached to any account is
// refused and nothing changes.
func (s *Server) handleIdentityLinkComplete(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	name := r.PathValue("provider")
	provider, ok := s.offered(w, name)
	if !ok || !s.linkAttemptAllowed(w, p, name) {
		return
	}
	var req identityLinkCompleteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, errors.New("code is required"))
		return
	}
	if !s.redirectAllowed(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, errors.New("redirect_uri is not on this controller's allowlist"))
		return
	}
	key, err := s.store.IdentityLinkStateKey()
	if err != nil {
		s.writeInternalError(w, r, "link complete", err)
		return
	}
	now := time.Now()
	st, reason := openLinkState(key, req.State, req.Verifier, name, p, now)
	if reason == "" {
		// safety: the used state is recorded in the store, so no replica and no
		// restart finishes the same flow twice.
		fresh, err := s.store.ConsumeIdentityLinkState(r.Context(), st.Nonce, time.Unix(st.Expires, 0), now)
		if err != nil {
			s.writeInternalError(w, r, "link state", err)
			return
		}
		if !fresh {
			reason = "this link was already used; start linking again"
		}
	}
	if reason != "" {
		s.refuseIdentityChange(w, p, http.StatusForbidden, "link_state_invalid", name, reason)
		return
	}
	label := providerLabels[name]
	profile, err := provider.SignIn(r.Context(), req.Code, req.Verifier, req.RedirectURI)
	switch {
	case errors.Is(err, errSignInUnverified):
		s.refuseIdentityChange(w, p, http.StatusForbidden, "provider_unverified", name,
			label+" has not verified this account's email address")
		return
	case errors.Is(err, errSignInRejected):
		s.refuseIdentityChange(w, p, http.StatusUnauthorized, "provider_rejected", name,
			label+" did not confirm the sign-in; start linking again")
		return
	case err != nil:
		s.logger.Warn("identity link failed", "provider", name, "error", err.Error())
		writeAuthError(w, http.StatusBadGateway, authErrorBody{
			Code: "provider_unreachable", Principal: p.label(),
			Message: label + " could not be reached to finish linking",
		})
		return
	}
	linked, err := s.store.LinkIdentity(r.Context(), p.AccountID, profile, now)
	switch {
	case errors.Is(err, store.ErrIdentityLinkedElsewhere):
		s.refuseIdentityChange(w, p, http.StatusConflict, "identity_linked_elsewhere", name,
			"that "+label+" sign-in is already linked to another Sparkwing account, so nothing changed. "+
				"Sparkwing does not combine accounts: invite one account into the other's team, "+
				"or delete the other account and then link its sign-in here")
		return
	case errors.Is(err, store.ErrIdentityAlreadyLinked):
		s.refuseIdentityChange(w, p, http.StatusConflict, "identity_already_linked", name,
			"that "+label+" sign-in is already linked to this account")
		return
	case errors.Is(err, store.ErrProviderAlreadyLinked):
		s.refuseIdentityChange(w, p, http.StatusConflict, "provider_already_linked", name,
			"this account already has a "+label+" sign-in; unlink it before linking another")
		return
	case err != nil:
		s.writeInternalError(w, r, "link identity", err)
		return
	}
	s.logger.Info("identity.linked", "account", p.AccountID, "provider", name, "subject", linked.Subject)
	writeJSON(w, http.StatusCreated, identityOut(linked))
}

// handleIdentityUnlink removes one of the caller's sign-in methods. The
// account keeps at least one, and every other session it holds ends.
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
	if !s.recentSignIn(w, p, name) {
		return
	}
	gone, ended, err := s.store.UnlinkIdentity(r.Context(), p.AccountID, name, p.session, time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("this account has no "+providerLabels[name]+" sign-in"))
		return
	case errors.Is(err, store.ErrLastSignInMethod):
		s.refuseIdentityChange(w, p, http.StatusConflict, "last_sign_in_method", name,
			"this is the account's only sign-in method; link another before unlinking it")
		return
	case err != nil:
		s.writeInternalError(w, r, "unlink identity", err)
		return
	}
	s.logger.Info("identity.unlinked", "account", p.AccountID, "provider", name, "subject", gone.Subject,
		"sessions_ended", ended)
	w.WriteHeader(http.StatusNoContent)
}
