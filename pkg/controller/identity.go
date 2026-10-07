package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type identityConfig struct {
	providers    map[string]signInProvider
	redirectURIs []string
	license      *license.License
	signup       signUpConfig
}

// WithGoogleSignIn offers Google sign-in through client. redirectURIs is the
// allowlist a sign-in's redirect URI must be on; the dashboard passes the URI
// it derived from the browser's Host, so this list is the check that the
// code is sent back to a dashboard this deployment runs.
func (s *Server) WithGoogleSignIn(client *googleauth.Client, redirectURIs []string) *Server {
	return s.withProvider(store.ProviderGoogle, googleProvider{client}, redirectURIs)
}

// WithGitHubSignIn offers GitHub sign-in through client, under the same
// redirect allowlist rule as [Server.WithGoogleSignIn].
func (s *Server) WithGitHubSignIn(client *githubauth.Client, redirectURIs []string) *Server {
	return s.withProvider(store.ProviderGitHub, githubProvider{client}, redirectURIs)
}

func (s *Server) withProvider(name string, p signInProvider, redirectURIs []string) *Server {
	if s.identity.providers == nil {
		s.identity.providers = map[string]signInProvider{}
	}
	s.identity.providers[name] = p
	for _, u := range redirectURIs {
		if !slices.Contains(s.identity.redirectURIs, u) {
			s.identity.redirectURIs = append(s.identity.redirectURIs, u)
		}
	}
	return s
}

// WithLicense installs a verified license. A nil license grants nothing, so
// the controller holds one team and offers no identity-provider sign-in.
func (s *Server) WithLicense(lic *license.License) *Server {
	s.identity.license = lic
	return s
}

// MultiTeam reports whether this controller may hold more than one team.
func (s *Server) MultiTeam() bool {
	return s.identity.license.Allows(license.FeatureMultiTeam, time.Now())
}

// Metering reports whether the signed license grants credit billing.
func (s *Server) Metering() bool {
	return s.identity.license.Allows(license.FeatureMetering, time.Now())
}

func (s *Server) signInProviders() []string {
	out := []string{}
	if !s.MultiTeam() {
		return out
	}
	for _, name := range []string{store.ProviderGoogle, store.ProviderGitHub} {
		if _, ok := s.identity.providers[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

type capabilitiesTeams struct {
	Enabled bool `json:"enabled"`
}

type capabilitiesAuth struct {
	Providers []string `json:"providers"`
}

type capabilitiesResp struct {
	Mode      string                       `json:"mode,omitempty"`
	Storage   *backend.CapabilitiesStorage `json:"storage,omitempty"`
	Features  []string                     `json:"features,omitempty"`
	ReadOnly  bool                         `json:"read_only,omitempty"`
	Teams     capabilitiesTeams            `json:"teams"`
	Billing   capabilitiesBilling          `json:"billing"`
	Auth      capabilitiesAuth             `json:"auth"`
	Claims    capabilitiesClaims           `json:"claims"`
	GitHubApp *capabilitiesGitHubApp       `json:"github_app,omitempty"`
}

type capabilitiesBilling struct {
	Enabled bool `json:"enabled"`
}

type capabilitiesGitHubApp struct {
	Slug         string `json:"slug"`
	SourceTokens bool   `json:"source_tokens"`
}

type capabilitiesClaims struct {
	AllowRepos bool `json:"allow_repos"`
	Profile    bool `json:"profile"`
}

// safety: unauthenticated, because a signed-out browser draws the sign-in page from it; it reports only
// whether teams exist, which providers to offer and which claim fields this controller reads.
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	var resp capabilitiesResp
	if d := s.dashboard; d != nil && d.Capabilities.Mode != "" {
		storage := d.Capabilities.Storage
		resp.Mode, resp.Storage, resp.Features, resp.ReadOnly = d.Capabilities.Mode, &storage, d.Capabilities.Features, d.Capabilities.ReadOnly
	}
	resp.Teams.Enabled = s.MultiTeam()
	resp.Billing.Enabled = s.Metering()
	resp.Auth.Providers = s.signInProviders()
	resp.Claims.AllowRepos = true
	resp.Claims.Profile = true
	if s.githubApp != nil {
		resp.GitHubApp = &capabilitiesGitHubApp{Slug: s.githubApp.client.Slug(), SourceTokens: true}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request, raw string, next http.Handler) {
	p, err := s.sessionPrincipal(r.Context(), raw, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrSessionBackend) {
			s.logger.Error("session.unavailable", "error", err.Error())
			setRetryAfter(w, authUnavailableRetryAfter)
			writeAuthError(w, http.StatusServiceUnavailable, authErrorBody{
				Code: "unavailable", Message: "authentication is temporarily unavailable",
			})
			return
		}
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{Code: "unauthenticated", Message: err.Error()})
		return
	}
	observeRequestPrincipal(p.Kind)
	ctx := contextWithPrincipal(r.Context(), p)
	stampPrincipal(ctx, p)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// safety: an account's scopes come from its membership as it stands now, so a demotion or removal
// takes effect on the next request rather than at session expiry.
func (s *Server) sessionPrincipal(ctx context.Context, raw string, now time.Time) (*Principal, error) {
	p, _, err := s.lookupSession(ctx, raw, now)
	return p, err
}

func (s *Server) lookupSession(ctx context.Context, raw string, now time.Time) (*Principal, *store.Session, error) {
	sess, err := s.store.LookupSessionAndRenew(ctx, raw, now, sessionTTL, s.sessionMaxLifetime)
	if err != nil {
		return nil, nil, err
	}
	if s.sessionExpired(sess.CreatedAt, now) {
		if err := s.store.DeleteSession(raw); err != nil {
			s.logger.Warn("deleting an expired session failed", "error", err.Error())
		}
		return nil, nil, errSessionLifetimeExceeded
	}
	p := &Principal{
		Name: sess.Principal, Kind: store.TokenKindUser, Team: sess.Team,
		Authed: now, session: raw, signedInAt: sess.CreatedAt,
	}
	if sess.AccountID == "" {
		p.Scopes = sess.Scopes
		return p, sess, nil
	}
	// safety: an account exists only under a multi-team license, so a session it opened stops
	// authenticating the moment the license is gone or expires, not only new sign-ins.
	if !s.MultiTeam() {
		return nil, nil, errAccountSessionsDisabled
	}
	p.AccountID = sess.AccountID
	role, err := s.memberRole(ctx, sess.Team, sess.AccountID)
	if err != nil {
		return nil, nil, err
	}
	p.Role = string(role)
	p.Scopes = ScopesForRole(role)
	return p, sess, nil
}

// safety: a missing team or membership answers the empty role, which carries no scope.
func (s *Server) memberRole(ctx context.Context, team store.Team, accountID string) (store.Role, error) {
	t, err := s.store.ForTeam(ctx, team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return "", nil
	}
	if err != nil {
		return "", errors.Join(store.ErrSessionBackend, err)
	}
	role, err := t.MemberRole(ctx, accountID)
	if errors.Is(err, store.ErrNotMember) {
		return "", nil
	}
	if err != nil {
		return "", errors.Join(store.ErrSessionBackend, err)
	}
	return role, nil
}

// safety: a bearer token names no human, so it cannot use the routes a membership decides.
func accountPrincipal(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.AccountID == "" {
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{
			Code: "unauthenticated", Message: "sign in to use this route",
		})
		return nil, false
	}
	return p, true
}

func (s *Server) teamMember(w http.ResponseWriter, r *http.Request, min store.Role) (*Principal, *store.Tenant, bool) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return nil, nil, false
	}
	if !store.Role(p.Role).AtLeast(min) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "forbidden", Principal: p.label(),
			Message: "this needs the " + string(min) + " role in the active team",
		})
		return nil, nil, false
	}
	t, err := s.store.ForTeam(r.Context(), p.Team)
	if err != nil {
		s.writeInternalError(w, r, "team handle", err)
		return nil, nil, false
	}
	return p, t, true
}

var errAccountSessionsDisabled = errors.New("sign-in is not enabled on this controller")

// safety: an account that lost its membership keeps a session so it can switch teams, and that session
// must not pass a route that admits any authenticated caller.
func refuseRoleless(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFromContext(r.Context()); ok && p.AccountID != "" && p.Role == "" {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code: "forbidden", Principal: p.label(), Message: "you are not a member of the active team",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randomURLToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Server) redirectAllowed(uri string) bool {
	return uri != "" && slices.Contains(s.identity.redirectURIs, uri)
}

func (s *Server) offeredProvider(name string) (signInProvider, error) {
	if p, ok := s.identity.providers[name]; ok && s.MultiTeam() {
		return p, nil
	}
	return nil, refuse(http.StatusNotFound, "", name+" sign-in is not enabled on this controller")
}

// safety: nothing is stored; the browser surface's __Host- flow cookie proves the same browser finishes the
// flow, and the redirect allowlist is this controller's own check.
func (s *Server) oauthBegin(name, redirectURI string) (oauthGrant, error) {
	provider, err := s.offeredProvider(name)
	if err != nil {
		return oauthGrant{}, err
	}
	if !s.redirectAllowed(redirectURI) {
		return oauthGrant{}, refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	state, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, err
	}
	verifier, err := randomURLToken()
	if err != nil {
		return oauthGrant{}, err
	}
	return oauthGrant{
		AuthorizeURL: provider.AuthorizeURL(state, verifier, redirectURI),
		State:        state,
		Verifier:     verifier,
	}, nil
}

type userJSONBody struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type teamRefJSON struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// safety: the refusal names none of the existing account's providers, because
// whoever holds the address now may not be that account's owner.
//
//lint:ignore ST1005 This error is displayed as complete sign-in guidance to the user.
var errAccountExistsSignIn = errors.New("An account with this email already exists. " + //nolint:staticcheck // shown to the person signing in
	"Sign in the way you did before, then link this provider from account settings.")

// safety: the verifier binds the code to the flow that started it, so a code injected into another
// browser's callback fails at the provider, because that browser holds another verifier.
func (s *Server) oauthSignIn(ctx context.Context, name, code, verifier, redirectURI string) (*store.Session, string, error) {
	provider, err := s.offeredProvider(name)
	if err != nil {
		return nil, "", err
	}
	if code == "" || verifier == "" {
		return nil, "", refuse(http.StatusBadRequest, "", "code and verifier are required")
	}
	if !s.redirectAllowed(redirectURI) {
		return nil, "", refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	profile, err := provider.SignIn(ctx, code, verifier, redirectURI)
	switch {
	case errors.Is(err, errSignInUnverified):
		return nil, "", refuse(http.StatusForbidden, "", name+" has not verified this account's email address")
	case errors.Is(err, errSignInRejected):
		s.logger.Info("sign-in rejected", "provider", name, "reason", err.Error())
		return nil, "", refuse(http.StatusUnauthorized, "", "that sign-in could not be verified; start again")
	case err != nil:
		s.logger.Warn("sign-in failed", "provider", name, "error", err.Error())
		return nil, "", refuse(http.StatusBadGateway, "", name+" could not be reached to finish the sign-in")
	}
	now := time.Now().UTC()
	res, err := s.store.ResolveSignIn(ctx, profile, s.signUpConditions(ctx), now)
	if errors.Is(err, store.ErrAccountExists) {
		return nil, "", refuse(http.StatusConflict, "", errAccountExistsSignIn.Error())
	}
	if err != nil {
		return nil, "", fmt.Errorf("sign-in resolve: %w", err)
	}
	s.observeSignUp(ctx, name, res, now)
	acct := res.Account
	raw, _, sess, err := s.store.CreateIdentityAccountSession(ctx, acct, acct.ActiveTeam,
		profile.Provider, profile.Subject, s.sessionInitialTTL(), now)
	if err != nil {
		if errors.Is(err, store.ErrIdentityUnlinked) {
			return nil, "", refuse(http.StatusUnauthorized, "", "that sign-in changed while it was being verified; start again")
		}
		return nil, "", fmt.Errorf("session create: %w", err)
	}
	s.logger.Info("signed in", "account", acct.ID, "provider", name,
		"new_account", res.NewAccount, "personal_team", string(res.PersonalTeam),
		"waitlisted", acct.Waitlisted)
	return sess, raw, nil
}

func (s *Server) teamRef(ctx context.Context, team store.Team, accountID string) (*teamRefJSON, error) {
	if team == "" {
		return nil, nil
	}
	role, err := s.memberRole(ctx, team, accountID)
	if err != nil || role == "" {
		return nil, err
	}
	info, err := s.store.TeamInfo(ctx, team)
	if err != nil {
		return nil, err
	}
	return &teamRefJSON{Slug: string(info.Slug), DisplayName: info.Label(), Role: string(role)}, nil
}

type invitationRefJSON struct {
	ID              string `json:"id"`
	TeamSlug        string `json:"team_slug"`
	TeamDisplayName string `json:"team_display_name"`
	Role            string `json:"role"`
}

type meResp struct {
	User        userJSONBody        `json:"user"`
	ActiveTeam  *teamRefJSON        `json:"active_team"`
	Memberships []teamRefJSON       `json:"memberships"`
	Invitations []invitationRefJSON `json:"invitations"`
	Waitlisted  bool                `json:"waitlisted"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	acct, err := s.store.Account(ctx, p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "me account", err)
		return
	}
	// safety: a session opened while the account held no team carries none, so
	// once an approval or another device gives it one, the session follows.
	if p.Team == "" && acct.ActiveTeam != "" {
		if err := s.switchTeam(ctx, p, acct.ActiveTeam); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeInternalError(w, r, "me session team", err)
			return
		}
		p.Team = acct.ActiveTeam
	}
	resp := meResp{
		User:        userJSONBody{ID: acct.ID, Email: acct.Email, Name: acct.Name},
		Memberships: []teamRefJSON{},
		Invitations: []invitationRefJSON{},
		Waitlisted:  acct.Waitlisted,
	}
	members, err := s.store.AccountMemberships(ctx, acct.ID)
	if err != nil {
		s.writeInternalError(w, r, "me memberships", err)
		return
	}
	for _, m := range members {
		ref := teamRefJSON{Slug: string(m.Team), DisplayName: store.TeamInfo{Slug: m.Team, DisplayName: m.DisplayName}.Label(), Role: string(m.Role)}
		resp.Memberships = append(resp.Memberships, ref)
		if m.Team == p.Team {
			active := ref
			resp.ActiveTeam = &active
		}
	}
	invites, err := s.store.OpenInvitationsForEmail(ctx, acct.Email, time.Now().UTC())
	if err != nil {
		s.writeInternalError(w, r, "me invitations", err)
		return
	}
	for _, inv := range invites {
		resp.Invitations = append(resp.Invitations, invitationRefJSON{
			ID: inv.ID, TeamSlug: string(inv.Team),
			TeamDisplayName: store.TeamInfo{Slug: inv.Team, DisplayName: inv.TeamDisplayName}.Label(),
			Role:            string(inv.Role),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type activeTeamReq struct {
	Slug string `json:"slug"`
}

func (s *Server) handleSetActiveTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	var req activeTeamReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	team := store.NormalizeTeam(store.Team(req.Slug))
	// safety: a team the account is not in answers 404, the same as a team
	// that does not exist, so the route does not report which slugs are taken.
	role, err := s.memberRole(r.Context(), team, p.AccountID)
	if err != nil {
		s.writeInternalError(w, r, "active team", err)
		return
	}
	if role == "" {
		writeError(w, http.StatusNotFound, errors.New("no team by that slug has you as a member"))
		return
	}
	if err := s.switchTeam(r.Context(), p, team); err != nil {
		s.writeInternalError(w, r, "active team", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) switchTeam(ctx context.Context, p *Principal, team store.Team) error {
	if err := s.store.SetActiveTeam(ctx, p.AccountID, team, time.Now().UTC()); err != nil {
		return err
	}
	if p.Team == team {
		return nil
	}
	return s.store.SwitchSessionTeam(ctx, p.session, p.AccountID, p.Team, team)
}

type createTeamReq struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type teamJSON struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	if !s.MultiTeam() {
		writeError(w, http.StatusForbidden, errors.New("this controller holds one team; creating another needs a multi-team license"))
		return
	}
	var req createTeamReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	slug := store.NormalizeTeam(store.Team(req.Slug))
	info, err := s.store.CreateTeam(r.Context(), p.AccountID, slug, req.DisplayName, time.Now().UTC())
	if err != nil {
		writeIdentityError(w, s, r, "create team", err)
		return
	}
	if p.Team != slug {
		if err := s.store.SwitchSessionTeam(r.Context(), p.session, p.AccountID, p.Team, slug); err != nil {
			s.writeInternalError(w, r, "create team switch", err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, teamJSON{Slug: string(info.Slug), DisplayName: info.Label(), Role: string(store.RoleOwner)})
}

// safety: another team's row answers 404, never 403, so a member learns nothing about other teams
// from the status code.
func writeIdentityError(w http.ResponseWriter, s *Server, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotMember):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrInvalidSlug), errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, store.ErrSlugTaken), errors.Is(err, store.ErrAlreadyMember),
		errors.Is(err, store.ErrInvitationOpen):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrRoleAboveOwn),
		errors.Is(err, store.ErrEmailMismatch), errors.Is(err, store.ErrTeamLimit), errors.Is(err, store.ErrTeamFull),
		errors.Is(err, store.ErrWaitlisted), errors.Is(err, store.ErrBindingLimit):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, store.ErrInvitationClosed):
		writeError(w, http.StatusGone, err)
	case errors.Is(err, store.ErrInvitationLimit):
		writeError(w, http.StatusTooManyRequests, err)
	default:
		s.writeInternalError(w, r, op, err)
	}
}
