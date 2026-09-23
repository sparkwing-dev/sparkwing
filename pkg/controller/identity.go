package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
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
	Teams     capabilitiesTeams      `json:"teams"`
	Auth      capabilitiesAuth       `json:"auth"`
	Claims    capabilitiesClaims     `json:"claims"`
	GitHubApp *capabilitiesGitHubApp `json:"github_app,omitempty"`
}

type capabilitiesGitHubApp struct {
	Slug         string `json:"slug"`
	SourceTokens bool   `json:"source_tokens"`
}

// capabilitiesClaims tells a runner which claim fields this controller reads,
// so a newer runner sends only what an older controller accepts.
type capabilitiesClaims struct {
	// AllowRepos reports that trigger and node claims take allow_repos.
	AllowRepos bool `json:"allow_repos"`
}

// safety: unauthenticated, because a signed-out browser draws the sign-in page from it; it reports only
// whether teams exist, which providers to offer and which claim fields this controller reads.
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	var resp capabilitiesResp
	resp.Teams.Enabled = s.MultiTeam()
	resp.Auth.Providers = s.signInProviders()
	resp.Claims.AllowRepos = true
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
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: p.Name})
	next.ServeHTTP(w, r.WithContext(ctx))
}

// safety: an account's scopes come from its membership as it stands now, so a demotion or removal
// takes effect on the next request rather than at session expiry.
func (s *Server) sessionPrincipal(ctx context.Context, raw string, now time.Time) (*Principal, error) {
	//nolint:contextcheck // LookupSession predates contexts on the session surface; handleSession calls it the same way
	sess, err := s.store.LookupSession(raw, now)
	if err != nil {
		return nil, err
	}
	if s.sessionExpired(sess.CreatedAt, now) {
		if err := s.store.DeleteSession(raw); err != nil {
			s.logger.Warn("deleting an expired session failed", "error", err.Error())
		}
		return nil, errSessionLifetimeExceeded
	}
	p := &Principal{
		Name: sess.Principal, Kind: store.TokenKindUser, Team: sess.Team,
		Authed: now, session: raw, signedInAt: sess.CreatedAt,
	}
	if sess.AccountID == "" {
		p.Scopes = sess.Scopes
		return p, nil
	}
	// safety: an account exists only under a multi-team license, so a session it opened stops
	// authenticating the moment the license is gone or expires, not only new sign-ins.
	if !s.MultiTeam() {
		return nil, errAccountSessionsDisabled
	}
	p.AccountID = sess.AccountID
	role, err := s.memberRole(ctx, sess.Team, sess.AccountID)
	if err != nil {
		return nil, err
	}
	p.Role = string(role)
	p.Scopes = ScopesForRole(role)
	return p, nil
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

func (s *Server) offered(w http.ResponseWriter, name string) (signInProvider, bool) {
	if p, ok := s.identity.providers[name]; ok && s.MultiTeam() {
		return p, true
	}
	writeError(w, http.StatusNotFound, errors.New(name+" sign-in is not enabled on this controller"))
	return nil, false
}

type oauthStartReq struct {
	RedirectURI string `json:"redirect_uri"`
}

type oauthStartResp struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

// safety: nothing is stored; the dashboard's __Host- cookie proves the same browser finishes the flow,
// and the redirect allowlist is this controller's own check.
func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	s.oauthStart(w, r, store.ProviderGoogle)
}

func (s *Server) handleGitHubStart(w http.ResponseWriter, r *http.Request) {
	s.oauthStart(w, r, store.ProviderGitHub)
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.offered(w, name)
	if !ok {
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
	state, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "oauth start", err)
		return
	}
	verifier, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "oauth start", err)
		return
	}
	writeJSON(w, http.StatusOK, oauthStartResp{
		AuthorizeURL: provider.AuthorizeURL(state, verifier, req.RedirectURI),
		State:        state,
		Verifier:     verifier,
	})
}

type oauthExchangeReq struct {
	Code        string `json:"code"`
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirect_uri"`
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

type oauthExchangeResp struct {
	SessionID  string       `json:"session_id"`
	CSRFToken  string       `json:"csrf_token"`
	ExpiresAt  int64        `json:"expires_at"`
	User       userJSONBody `json:"user"`
	ActiveTeam *teamRefJSON `json:"active_team"`
	Waitlisted bool         `json:"waitlisted"`
}

func (s *Server) handleGoogleExchange(w http.ResponseWriter, r *http.Request) {
	s.oauthExchange(w, r, store.ProviderGoogle)
}

func (s *Server) handleGitHubExchange(w http.ResponseWriter, r *http.Request) {
	s.oauthExchange(w, r, store.ProviderGitHub)
}

// safety: the verifier binds the code to the flow that started it, so a code injected into another
// browser's callback fails at the provider, because that browser holds another verifier.
func (s *Server) oauthExchange(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.offered(w, name)
	if !ok {
		return
	}
	var req oauthExchangeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Code == "" || req.Verifier == "" {
		writeError(w, http.StatusBadRequest, errors.New("code and verifier are required"))
		return
	}
	if !s.redirectAllowed(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, errors.New("redirect_uri is not on this controller's allowlist"))
		return
	}
	profile, err := provider.SignIn(r.Context(), req.Code, req.Verifier, req.RedirectURI)
	switch {
	case errors.Is(err, errSignInUnverified):
		writeError(w, http.StatusForbidden, errors.New(name+" has not verified this account's email address"))
		return
	case errors.Is(err, errSignInRejected):
		s.logger.Info("sign-in rejected", "provider", name, "reason", err.Error())
		writeError(w, http.StatusUnauthorized, errors.New("that sign-in could not be verified; start again"))
		return
	case err != nil:
		s.logger.Warn("sign-in failed", "provider", name, "error", err.Error())
		writeError(w, http.StatusBadGateway, errors.New(name+" could not be reached to finish the sign-in"))
		return
	}
	now := time.Now().UTC()
	res, err := s.store.ResolveSignIn(r.Context(), profile, s.signUpConditions(r.Context()), now)
	if err != nil {
		s.writeInternalError(w, r, "sign-in resolve", err)
		return
	}
	s.observeSignUp(r.Context(), name, res, now)
	acct := res.Account
	raw, csrf, sess, err := s.store.CreateAccountSession(r.Context(), acct, acct.ActiveTeam, sessionTTL, now)
	if err != nil {
		s.writeInternalError(w, r, "session create", err)
		return
	}
	s.logger.Info("signed in", "account", acct.ID, "provider", name,
		"new_account", res.NewAccount, "linked", res.Linked, "personal_team", string(res.PersonalTeam),
		"waitlisted", acct.Waitlisted)
	active, err := s.teamRef(r.Context(), acct.ActiveTeam, acct.ID)
	if err != nil {
		s.writeInternalError(w, r, "sign-in team", err)
		return
	}
	writeJSON(w, http.StatusOK, oauthExchangeResp{
		SessionID: raw, CSRFToken: csrf, ExpiresAt: sess.ExpiresAt.Unix(),
		User: userJSONBody{ID: acct.ID, Email: acct.Email, Name: acct.Name}, ActiveTeam: active,
		Waitlisted: acct.Waitlisted,
	})
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
