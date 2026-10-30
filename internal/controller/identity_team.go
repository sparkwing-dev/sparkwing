package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const runnerPrincipalPrefix = "agent:"

type renameTeamReq struct {
	DisplayName string `json:"display_name"`
}

func (s *Server) handleRenameTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req renameTeamReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	info, err := writer.RenameAuthorizedTeam(r.Context(), a, req.DisplayName)
	if err != nil {
		return accountMutationError(err)
	}
	if info.Slug != store.Team(a.Target.Team) {
		return routepolicy.MissingEndpointPolicy()
	}
	writeJSON(w, http.StatusOK, teamJSON{Slug: string(info.Slug), DisplayName: info.Label(), Role: string(store.RoleOwner)})
	return nil
}

type memberJSON struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type invitationJSON struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	invites, err := t.Invitations(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]invitationJSON, 0, len(invites))
	for _, inv := range invites {
		out = append(out, invitationJSON{ID: inv.ID, Email: inv.Email, Role: string(inv.Role), ExpiresAt: inv.ExpiresAt.Unix()})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type inviteReq struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteResp struct {
	ID        string `json:"id"`
	AcceptURL string `json:"accept_url"`
	EmailSent bool   `json:"email_sent"`
	Warning   string `json:"warning,omitempty"`
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req inviteReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	role := store.Role(req.Role)
	if !role.Valid() {
		return unknownRole(role)
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	inv, err := writer.CreateAuthorizedInvitation(r.Context(), a, req.Email, role, key)
	if err != nil {
		return accountMutationError(err)
	}
	if inv.Team != store.Team(a.Target.Team) {
		return routepolicy.MissingEndpointPolicy()
	}
	noteAuditTarget(r.Context(), "invitation_id", inv.ID)
	noteAuditTarget(r.Context(), "role", string(inv.Role))
	accept := s.acceptURL(inv.ID)
	sent := s.mailInvitation(r.Context(), a, inv, accept)
	writeJSON(w, http.StatusCreated, inviteResp{ID: inv.ID, AcceptURL: accept, EmailSent: sent, Warning: inv.Warning})
	return nil
}

// WithMailer sends invitation emails through m. Without one, the controller
// logs that it sent nothing and the owner hands the accept link on.
func (s *Server) WithMailer(m mailer.Mailer) *Server {
	s.mailer = m
	return s
}

// safety: a failed or refused send leaves the invitation standing, because the
// owner still holds its accept link; only the email is lost, and the response says so.
func (s *Server) mailInvitation(ctx context.Context, a capability.Authorized, inv store.Invitation, accept string) bool {
	if s.mailer == nil {
		if err := (mailer.Log{Logger: s.logger}).Send(ctx, mailer.Message{To: inv.Email, Subject: "team invitation"}); err != nil {
			s.logger.Warn("invitation email: log", "err", err)
		}
		return false
	}
	ok, err := s.operator.ClaimInvitationEmail(ctx, inv.ID)
	if err != nil {
		s.logger.Warn("invitation email: claim", "invitation", inv.ID, "err", err)
		return false
	}
	if !ok {
		s.logger.Info("invitation email withheld: the address reached its daily limit", "invitation", inv.ID)
		return false
	}
	inviter := a.Principal.Capability.Subject.ID
	if acct, err := s.operator.Account(ctx, a.Principal.Capability.Subject.ID); err == nil && strings.TrimSpace(acct.Name) != "" {
		inviter = acct.Name
	}
	team := string(inv.Team)
	if tenant, err := s.tenantForTeam(ctx, inv.Team); err == nil {
		if info, err := tenant.Info(ctx); err == nil {
			team = info.Label()
		}
	}
	msg, err := mailer.InvitationMessage(inv.Email, mailer.Invitation{
		InviterName: inviter, TeamName: team, Role: string(inv.Role), AcceptURL: accept, ExpiresAt: inv.ExpiresAt,
	})
	if err != nil {
		s.logger.Warn("invitation email: render", "invitation", inv.ID, "err", err)
		return false
	}
	if err := s.mailer.Send(ctx, msg); err != nil {
		s.logger.Warn("invitation email: send", "invitation", inv.ID, "err", err)
		return false
	}
	return true
}

// safety: the dashboard origin comes from the first redirect allowlist entry, the one host this deployment
// already trusts to receive a signed-in browser.
func (s *Server) acceptURL(id string) string {
	path := "/invitations?id=" + url.QueryEscape(id)
	if len(s.identity.redirectURIs) == 0 {
		return path
	}
	u, err := url.Parse(s.identity.redirectURIs[0])
	if err != nil {
		return path
	}
	return u.Scheme + "://" + u.Host + path
}

func (s *Server) handleDeleteInvitation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.DeleteAuthorizedInvitation(r.Context(), a, r.PathValue("invitation")); err != nil {
		return accountMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	acc, err := writer.AcceptAccountInvitation(r.Context(), a, r.PathValue("invitation"))
	if err != nil {
		return invitationRefusal(err)
	}
	if acc.Admitted {
		observeSignUpOutcome("admitted", "invitation")
		s.logger.Info("signup.admitted_by_invitation", "account", a.Principal.Capability.Subject.ID, "team", string(acc.Team))
	}
	if g := acc.GateClosed; g != nil {
		observeSignUpGateClosed(g.Source)
		s.logger.Warn("signup.gate_closed", "source", g.Source, "reason", g.Reason)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// safety: the store finds an invitation only through an address the caller owns, so a missing one and one sent
// elsewhere give the same answer and the expiry and use reasons describe only the caller's own invitations.
func invitationRefusal(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return reasonedRefusal(apierr.NotFound, invitationNotFound, "this invitation is not for the signed-in account or no longer exists")
	case errors.Is(err, store.ErrInvitationClosed):
		return reasonedRefusal(apierr.Conflict, invitationClosed, "this invitation has expired or was already used")
	case errors.Is(err, store.ErrTeamFull):
		return reasonedRefusal(apierr.Forbidden, invitationTeamFull, err.Error())
	}
	return accountMutationError(err)
}

const (
	invitationClosed   = "invitation_closed"
	invitationNotFound = "invitation_not_found"
	invitationTeamFull = "team_full"
)

const maxRunnerRepos = 32

// safety: a workflow pointing at the request's Host would bind runners to
// whatever name the caller used, over plain http, so without a declared
// origin there is no workflow to hand out.
func (s *Server) publicOrigin() (string, error) {
	if origin, ok := s.deployment.PublicOrigin(); ok {
		return origin, nil
	}
	return "", &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "this controller has no --external-url, so it cannot give a GitHub Actions workflow a controller to reach"}
}
