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

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type identityConfig struct {
	providers    map[string]signInProvider
	redirectURIs []string
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

func (s *Server) signInProviders() []string {
	out := []string{}
	if !s.deployment.MultiTeam() {
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
	Logs      capabilitiesLogs             `json:"logs"`
	GitHubApp *capabilitiesGitHubApp       `json:"github_app,omitempty"`
}

type capabilitiesLogs struct {
	Segments bool `json:"segments"`
}

type capabilitiesBilling struct {
	Enabled bool `json:"enabled"`
}

type capabilitiesGitHubApp struct {
	Slug string `json:"slug"`
}

type capabilitiesClaims struct {
	AllowRepos bool `json:"allow_repos"`
	Profile    bool `json:"profile"`
}

// safety: public discovery exposes features and engine support without tenant data.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if versions, present := r.URL.Query()["engine_version"]; present {
		engine := s.engineController
		result, err := engine.compatibility(r.Context(), versions[0])
		if err != nil {
			routepolicy.WritePolicyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var resp capabilitiesResp
	if d := s.dashboard; d != nil && d.Capabilities.Mode != "" {
		storage := d.Capabilities.Storage
		resp.Mode, resp.Storage, resp.Features, resp.ReadOnly = d.Capabilities.Mode, &storage, d.Capabilities.Features, d.Capabilities.ReadOnly
	}
	resp.Teams.Enabled = s.deployment.MultiTeam()
	resp.Billing.Enabled = s.deployment.Metering()
	resp.Auth.Providers = s.signInProviders()
	resp.Claims.AllowRepos = true
	resp.Claims.Profile = true
	if s.githubApp != nil {
		resp.GitHubApp = &capabilitiesGitHubApp{Slug: s.githubApp.client.Slug()}
	}
	writeJSON(w, http.StatusOK, resp)
}

// safety: a missing team or membership answers the empty role, which carries no scope.
func (s *Server) memberRole(ctx context.Context, team store.Team, accountID string) (store.Role, error) {
	if team == "" {
		return "", nil
	}
	t, err := s.tenantForTeam(ctx, team)
	var failure *apierr.Error
	if errors.As(err, &failure) && failure.Code == apierr.NotFound {
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

var errAccountSessionsDisabled = errors.New("sign-in is not enabled on this controller")

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
	if p, ok := s.identity.providers[name]; ok && s.deployment.MultiTeam() {
		return p, nil
	}
	return nil, refuse(http.StatusNotFound, refusedUnavailable, name+" sign-in is not enabled on this controller")
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
		return nil, "", refuse(http.StatusForbidden, refusedEmailUnverified, name+" has not verified this account's email address")
	case errors.Is(err, errSignInRejected):
		s.logger.Info("sign-in rejected", "provider", name)
		return nil, "", refuse(http.StatusUnauthorized, refusedExpired, "that sign-in could not be verified; start again")
	case err != nil:
		s.logger.Warn("sign-in failed", "provider", name)
		return nil, "", refuse(http.StatusBadGateway, refusedUnavailable, name+" could not be reached to finish the sign-in")
	}
	now := time.Now().UTC()
	res, err := s.operator.ResolveSignIn(ctx, profile, s.signUpConditions(ctx))
	if errors.Is(err, store.ErrAccountExists) {
		return nil, "", refuse(http.StatusConflict, refusedAccountExists, errAccountExistsSignIn.Error())
	}
	if err != nil {
		return nil, "", fmt.Errorf("sign-in resolve: %w", err)
	}
	s.observeSignUp(ctx, name, res, now)
	acct := res.Account
	raw, _, sess, err := s.operator.CreateIdentityAccountSession(ctx, acct, acct.ActiveTeam,
		profile.Provider, profile.Subject, s.sessionInitialTTL())
	if err != nil {
		if errors.Is(err, store.ErrIdentityUnlinked) {
			return nil, "", refuse(http.StatusUnauthorized, refusedExpired, "that sign-in changed while it was being verified; start again")
		}
		return nil, "", fmt.Errorf("session create: %w", err)
	}
	s.logger.Info("signed in", "account", acct.ID, "provider", name,
		"new_account", res.NewAccount, "personal_team", string(res.PersonalTeam),
		"waitlisted", acct.Waitlisted)
	return sess, raw, nil
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

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	reader, err := s.accountReader()
	if err != nil {
		return err
	}
	ctx := r.Context()
	accountID := a.Principal.Capability.Subject.ID
	acct, err := reader.Account(ctx, accountID)
	if err != nil {
		return engineLookupError(err)
	}
	if acct.ID != accountID {
		return errors.New("inconsistent persisted account")
	}
	resp := meResp{
		User:        userJSONBody{ID: acct.ID, Email: acct.Email, Name: acct.Name},
		Memberships: []teamRefJSON{},
		Invitations: []invitationRefJSON{},
		Waitlisted:  acct.Waitlisted,
	}
	members, err := reader.AccountMemberships(ctx, accountID)
	if err != nil {
		return engineLookupError(err)
	}
	for _, m := range members {
		if m.AccountID != accountID {
			return errors.New("inconsistent persisted account membership")
		}
		ref := teamRefJSON{Slug: string(m.Team), DisplayName: store.TeamInfo{Slug: m.Team, DisplayName: m.DisplayName}.Label(), Role: string(m.Role)}
		resp.Memberships = append(resp.Memberships, ref)
		if string(m.Team) == a.Principal.Capability.Team {
			active := ref
			resp.ActiveTeam = &active
		}
	}
	invites, err := reader.OpenInvitationsForAccount(ctx, accountID)
	if err != nil {
		return engineLookupError(err)
	}
	for _, inv := range invites {
		resp.Invitations = append(resp.Invitations, invitationRefJSON{
			ID: inv.ID, TeamSlug: string(inv.Team),
			TeamDisplayName: store.TeamInfo{Slug: inv.Team, DisplayName: inv.TeamDisplayName}.Label(),
			Role:            string(inv.Role),
		})
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

type activeTeamReq struct {
	Slug string `json:"slug"`
}

func (s *Server) handleSetActiveTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req activeTeamReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	team := store.NormalizeTeam(store.Team(req.Slug))
	if team == "" {
		return invalidEngineBody("team slug is required")
	}
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	if err := writer.SelectAccountTeam(r.Context(), a, team); err != nil {
		return accountMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
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

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if !s.deployment.MultiTeam() {
		return apierr.Authorization(apierr.ReasonMissingGrant, "creating a team requires a multi-team license")
	}
	var req createTeamReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	team := store.NormalizeTeam(store.Team(req.Slug))
	if err := store.ValidateSlug(string(team)); err != nil {
		return accountMutationError(err)
	}
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	info, err := writer.CreateAccountTeam(r.Context(), a, store.TeamInfo{Slug: team, DisplayName: req.DisplayName})
	if err != nil {
		return accountMutationError(err)
	}
	if info.Slug != team {
		return errors.New("inconsistent created team")
	}
	writeJSON(w, http.StatusCreated, teamJSON{Slug: string(info.Slug), DisplayName: info.Label(), Role: string(store.RoleOwner)})
	return nil
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
