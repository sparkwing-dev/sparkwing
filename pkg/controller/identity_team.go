package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the scope set docs/auth.md gives a runner; a runner token minted from team settings gets no more.
var runnerTokenScopes = []string{ScopeNodesClaim, ScopeTriggersClaim, ScopeRunsState, ScopeSecretsRead, ScopeLogsWrite}

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

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	members, err := t.Members(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list members", err)
		return
	}
	out := make([]memberJSON, 0, len(members))
	for _, m := range members {
		out = append(out, memberJSON{UserID: m.AccountID, Email: m.Email, Name: m.Name, Role: string(m.Role)})
	}
	writeJSON(w, http.StatusOK, out)
}

type setRoleReq struct {
	Role string `json:"role"`
}

func (s *Server) handleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req setRoleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	revoked, err := t.SetMemberRole(r.Context(), p.AccountID, r.PathValue("user_id"), store.Role(req.Role), time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "set member role", err)
		return
	}
	s.invalidateRevokedTokens(revoked)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	subject := r.PathValue("user_id")
	if subject != p.AccountID && !p.HasScope(ScopeTeamAdmin) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "missing_scope", MissingScope: ScopeTeamAdmin, Principal: p.label(),
			Message: "removing another member needs the owner role",
		})
		return
	}
	revoked, err := t.RemoveMember(r.Context(), p.AccountID, subject, time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "remove member", err)
		return
	}
	s.invalidateRevokedTokens(revoked)
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	invites, err := t.Invitations(r.Context(), time.Now())
	if err != nil {
		s.writeInternalError(w, r, "list invitations", err)
		return
	}
	out := make([]invitationJSON, 0, len(invites))
	for _, inv := range invites {
		out = append(out, invitationJSON{ID: inv.ID, Email: inv.Email, Role: string(inv.Role), ExpiresAt: inv.ExpiresAt.Unix()})
	}
	writeJSON(w, http.StatusOK, out)
}

type inviteReq struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteResp struct {
	ID        string `json:"id"`
	AcceptURL string `json:"accept_url"`
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
	writeJSON(w, http.StatusCreated, inviteResp{ID: inv.ID, AcceptURL: s.acceptURL(inv.ID)})
}

// safety: the dashboard origin comes from the first redirect allowlist entry, the one host this deployment
// already trusts to receive a signed-in browser; no mail is sent, so the owner hands this link on.
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

func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := accountPrincipal(w, r)
	if !ok {
		return
	}
	team, err := s.store.AcceptInvitation(r.Context(), p.AccountID, r.PathValue("id"), time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "accept invitation", err)
		return
	}
	if p.Team != team {
		if err := s.store.SwitchSessionTeam(r.Context(), p.session, p.AccountID, p.Team, team); err != nil {
			s.writeInternalError(w, r, "accept invitation switch", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type runnerTokenReq struct {
	Name string `json:"name"`
}

type runnerTokenResp struct {
	Token   string `json:"token"`
	Prefix  string `json:"prefix"`
	Command string `json:"command"`
}

type runnerTokenJSON struct {
	Prefix     string `json:"prefix"`
	Name       string `json:"name"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  *int64 `json:"expires_at"`
	LastUsedAt *int64 `json:"last_used_at"`
}

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

func (s *Server) handleCreateRunnerToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleEditor)
	if !ok {
		return
	}
	var req runnerTokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	// safety: the name lands in a shell command the dashboard shows, so it is held to a charset no shell expands.
	if !validRunnerName(name) {
		writeError(w, http.StatusBadRequest, errors.New("name is 1 to 63 letters, digits, '-', '_' or '.'"))
		return
	}
	raw, tok, err := t.CreateRunnerToken(r.Context(), runnerPrincipalPrefix+name, runnerTokenScopes, p.AccountID, time.Now().UTC())
	if errors.Is(err, store.ErrRunnerTokenLimit) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "mint runner token", err)
		return
	}
	s.logger.Info("runner token minted", "team", string(p.Team), "prefix", tok.Prefix, "by", p.AccountID)
	writeJSON(w, http.StatusCreated, runnerTokenResp{
		Token: raw, Prefix: tok.Prefix,
		Command: "SPARKWING_AGENT_TOKEN=" + raw + " " + runnerConnectArgs(s.controllerURL(r), s.logsURL, name),
	})
}

// runnerConnectArgs is the command that turns a machine into one of the team's
// runners: it claims triggered runs as well as their nodes, fetches each run's
// source itself with the machine's own git credentials (there is no --gitcache,
// since the git cache is the operator's), ships logs where the dashboard reads
// them, and keeps claiming until stopped. A co-located deployment announces no
// logs URL because the controller serves the logs routes itself.
func runnerConnectArgs(controllerURL, logsURL, name string) string {
	if logsURL == "" {
		logsURL = controllerURL
	}
	return "sparkwing-runner runner --controller " + controllerURL +
		" --logs " + strings.TrimRight(logsURL, "/") +
		" --also-claim-triggers --max-claims-before-restart 0" +
		" --holder-prefix " + name
}

func (s *Server) controllerURL(r *http.Request) string {
	if s.externalURL != "" {
		return strings.TrimRight(s.externalURL, "/")
	}
	return "http://" + r.Host
}

func (s *Server) handleListRunnerTokens(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleEditor)
	if !ok {
		return
	}
	toks, err := t.RunnerTokens(r.Context(), time.Now())
	if err != nil {
		s.writeInternalError(w, r, "list runner tokens", err)
		return
	}
	out := make([]runnerTokenJSON, 0, len(toks))
	for _, tok := range toks {
		row := runnerTokenJSON{
			Prefix: tok.Prefix, Name: strings.TrimPrefix(tok.Principal, runnerPrincipalPrefix),
			CreatedBy: tok.CreatedBy, CreatedAt: tok.CreatedAt.Unix(),
		}
		if tok.ExpiresAt != nil {
			v := tok.ExpiresAt.Unix()
			row.ExpiresAt = &v
		}
		if tok.LastUsedAt != nil {
			v := tok.LastUsedAt.Unix()
			row.LastUsedAt = &v
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeRunnerToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleEditor)
	if !ok {
		return
	}
	prefix := r.PathValue("prefix")
	tok, err := t.RunnerToken(r.Context(), prefix)
	if err != nil {
		writeIdentityError(w, s, r, "revoke runner token", err)
		return
	}
	if !p.HasScope(ScopeTeamAdmin) && tok.CreatedBy != p.AccountID {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "forbidden", Principal: p.label(),
			Message: "an editor revokes only the runner tokens they minted",
		})
		return
	}
	if err := t.RevokeRunnerToken(r.Context(), prefix, time.Now()); err != nil {
		writeIdentityError(w, s, r, "revoke runner token", err)
		return
	}
	s.auth.Invalidate(prefix)
	w.WriteHeader(http.StatusNoContent)
}
