package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type teamTokenReq struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	Repos  []string `json:"repos"`
}

type teamTokenResp struct {
	Token     string            `json:"token"`
	Prefix    string            `json:"prefix"`
	Family    store.TokenFamily `json:"family"`
	Name      string            `json:"name,omitempty"`
	Scopes    []string          `json:"scopes"`
	ExpiresAt int64             `json:"expires_at"`
	Profile   string            `json:"profile,omitempty"`
	Setup     string            `json:"setup,omitempty"`
	Run       string            `json:"run,omitempty"`
	Command   string            `json:"command,omitempty"`
}

type teamTokenJSON struct {
	Prefix         string            `json:"prefix"`
	Name           string            `json:"name"`
	Family         store.TokenFamily `json:"family"`
	Scopes         []string          `json:"scopes"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      int64             `json:"created_at"`
	ExpiresAt      *int64            `json:"expires_at"`
	LastUsedAt     *int64            `json:"last_used_at"`
	GitCredentials bool              `json:"git_credentials"`
}

func (s *Server) handleCreateTeamToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	var req teamTokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	family, err := store.TeamTokenFamily(req.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if family == store.TokenFamilyMember {
		s.createMemberToken(w, r, p, t, req)
		return
	}
	if !store.Role(p.Role).AtLeast(store.RoleEditor) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "forbidden", Principal: p.label(), Message: "a machine token needs the editor role in the active team",
		})
		return
	}
	s.createMachineToken(w, r, p, t, req)
}

func (s *Server) createMemberToken(w http.ResponseWriter, r *http.Request, p *Principal, t *store.Tenant, req teamTokenReq) {
	if req.Name != "" || len(req.Repos) > 0 {
		writeError(w, http.StatusBadRequest, errors.New("name and repos describe a machine token; a member token takes only scopes"))
		return
	}
	raw, tok, err := t.CreateMemberToken(r.Context(), p.Name, req.Scopes, p.AccountID, time.Now().UTC())
	if errors.Is(err, store.ErrCLITokenLimit) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "mint member token", err)
		return
	}
	s.logger.Info("member token minted", "team", string(p.Team), "prefix", tok.Prefix, "by", p.AccountID)
	profile := string(p.Team)
	writeJSON(w, http.StatusCreated, teamTokenResp{
		Token: raw, Prefix: tok.Prefix, Family: store.TokenFamilyMember, Scopes: tok.Scopes,
		ExpiresAt: tok.ExpiresAt.Unix(), Profile: profile,
		Setup: "sparkwing cloud connect --controller " + s.controllerURL(r) + " --name " + profile + " --token-stdin",
		Run:   "sparkwing pipeline trigger <pipeline> --profile " + profile,
	})
}

func (s *Server) createMachineToken(w http.ResponseWriter, r *http.Request, p *Principal, t *store.Tenant, req teamTokenReq) {
	name := strings.TrimSpace(req.Name)
	// safety: the name lands in a shell command the dashboard shows, so it is held to a charset no shell expands.
	if !validRunnerName(name) {
		writeError(w, http.StatusBadRequest, errors.New("name is 1 to 63 letters, digits, '-', '_' or '.'"))
		return
	}
	if len(req.Repos) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("repos names the repositories this machine may build, "+
			"e.g. github.com/acme/*; the machine runs their pipeline code as the user who starts it"))
		return
	}
	if len(req.Repos) > maxRunnerRepos {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repos holds at most %d patterns", maxRunnerRepos))
		return
	}
	allow, err := sourceurl.ParseRepoAllowlist(req.Repos)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repos: %w", err))
		return
	}
	raw, tok, err := t.CreateMachineToken(r.Context(), name, req.Scopes, p.AccountID, time.Now().UTC())
	if errors.Is(err, store.ErrRunnerTokenLimit) || errors.Is(err, store.ErrAgentNameTaken) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "mint machine token", err)
		return
	}
	s.logger.Info("machine token minted", "team", string(p.Team), "prefix", tok.Prefix, "by", p.AccountID)
	writeJSON(w, http.StatusCreated, teamTokenResp{
		Token: raw, Prefix: tok.Prefix, Family: store.TokenFamilyMachine, Name: name, Scopes: tok.Scopes,
		ExpiresAt: tok.ExpiresAt.Unix(),
		Command:   runnerTokenSetup(raw) + " && " + runnerConnectArgs(s.controllerURL(r), name, allow, tok.Scopes),
	})
}

func (s *Server) handleListTeamTokens(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	now := time.Now()
	toks, err := t.TeamTokens(r.Context(), p.AccountID, store.Role(p.Role).AtLeast(store.RoleEditor), now)
	if err != nil {
		s.writeInternalError(w, r, "list team tokens", err)
		return
	}
	optedIn, err := t.GitCredentialMachines(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list git credential machines", err)
		return
	}
	out := make([]teamTokenJSON, 0, len(toks))
	for _, tok := range toks {
		row := teamTokenJSON{
			Prefix: tok.Prefix, Family: tok.Family(), Scopes: tok.Scopes,
			CreatedBy: tok.CreatedBy, CreatedAt: tok.CreatedAt.Unix(), GitCredentials: optedIn[tok.Prefix],
		}
		if row.Family == store.TokenFamilyMachine {
			row.Name = strings.TrimPrefix(tok.Principal, runnerPrincipalPrefix)
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

func (s *Server) handleRevokeTeamToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	prefix := r.PathValue("prefix")
	now := time.Now()
	tok, err := t.TeamToken(r.Context(), prefix)
	if err != nil {
		writeIdentityError(w, s, r, "revoke team token", err)
		return
	}
	own := tok.CreatedBy == p.AccountID
	machine := tok.Family() == store.TokenFamilyMachine
	// safety: a token the caller could not list answers as no token, so a prefix probe learns nothing.
	if !tok.IsValid(now) || (!own && !(machine && store.Role(p.Role).AtLeast(store.RoleEditor))) {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if !own && !p.HasScope(ScopeTeamAdmin) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "forbidden", Principal: p.label(),
			Message: "an editor revokes only the machine tokens they minted",
		})
		return
	}
	if err := t.RevokeTeamToken(r.Context(), prefix, now); err != nil {
		writeIdentityError(w, s, r, "revoke team token", err)
		return
	}
	s.auth.Invalidate(prefix)
	w.WriteHeader(http.StatusNoContent)
}
