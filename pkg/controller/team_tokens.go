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
	if family != store.TokenFamilyMachine {
		writeError(w, http.StatusBadRequest, errors.New("machine enrollment requires claim scopes"))
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
