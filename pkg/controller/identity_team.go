package controller

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const runnerPrincipalPrefix = "agent:"

type renameTeamReq struct {
	DisplayName string `json:"display_name"`
}

func (s *Server) handleRenameTeam(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req renameTeamReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := t.Rename(r.Context(), req.DisplayName, time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "rename team", err)
		return
	}
	writeJSON(w, http.StatusOK, teamJSON{Slug: string(info.Slug), DisplayName: info.Label(), Role: p.Role})
}

type memberJSON struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func (s *Server) invalidateRevokedTokens(prefixes []string) {
	for _, prefix := range prefixes {
		s.auth.Invalidate(prefix)
	}
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
	invites, err := t.Invitations(r.Context(), time.Now())
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
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req inviteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inv, err := t.CreateInvitation(r.Context(), p.AccountID, req.Email, store.Role(req.Role), time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "invite", err)
		return
	}
	noteAuditTarget(r.Context(), "invitation_id", inv.ID)
	noteAuditTarget(r.Context(), "role", string(inv.Role))
	accept := s.acceptURL(inv.ID)
	sent := s.mailInvitation(r.Context(), p, t, inv, accept)
	writeJSON(w, http.StatusCreated, inviteResp{ID: inv.ID, AcceptURL: accept, EmailSent: sent})
}

// WithMailer sends invitation emails through m. Without one, the controller
// logs that it sent nothing and the owner hands the accept link on.
func (s *Server) WithMailer(m mailer.Mailer) *Server {
	s.mailer = m
	return s
}

// safety: a failed or refused send leaves the invitation standing, because the
// owner still holds its accept link; only the email is lost, and the response says so.
func (s *Server) mailInvitation(ctx context.Context, p *Principal, t *store.Tenant, inv store.Invitation, accept string) bool {
	if s.mailer == nil {
		if err := (mailer.Log{Logger: s.logger}).Send(ctx, mailer.Message{To: inv.Email, Subject: "team invitation"}); err != nil {
			s.logger.Warn("invitation email: log", "err", err)
		}
		return false
	}
	ok, err := s.store.ClaimInvitationEmail(ctx, inv.ID, time.Now())
	if err != nil {
		s.logger.Warn("invitation email: claim", "invitation", inv.ID, "err", err)
		return false
	}
	if !ok {
		s.logger.Info("invitation email withheld: the address reached its daily limit", "invitation", inv.ID)
		return false
	}
	inviter := p.Name
	if acct, err := s.store.Account(ctx, p.AccountID); err == nil && strings.TrimSpace(acct.Name) != "" {
		inviter = acct.Name
	}
	team := string(t.Team())
	if info, err := t.Info(ctx); err == nil {
		team = info.Label()
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

func (s *Server) handleDeleteInvitation(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	if err := t.DeleteInvitation(r.Context(), r.PathValue("id"), time.Now()); err != nil {
		writeIdentityError(w, s, r, "delete invitation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	acc, err := writer.AcceptAccountInvitation(r.Context(), a, r.PathValue("invitation"))
	if err != nil {
		return accountMutationError(err)
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

const maxRunnerRepos = 32

func validRunnerName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func (s *Server) controllerURL(r *http.Request) string {
	if s.externalURL != "" {
		return strings.TrimRight(s.externalURL, "/")
	}
	return "http://" + r.Host
}
