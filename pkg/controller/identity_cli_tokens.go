package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cliTokenResp struct {
	Token     string   `json:"token"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
	Profile   string   `json:"profile"`
	Setup     string   `json:"setup"`
	Run       string   `json:"run"`
}

type cliTokenJSON struct {
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	CreatedAt  int64    `json:"created_at"`
	ExpiresAt  *int64   `json:"expires_at"`
	LastUsedAt *int64   `json:"last_used_at"`
}

func cliTokenScopes(role store.Role) []string {
	return store.CLITokenScopes(role)
}

func (s *Server) handleCreateCLIToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	scopes := cliTokenScopes(store.Role(p.Role))
	raw, tok, err := t.CreateCLIToken(r.Context(), p.Name, scopes, p.AccountID, time.Now().UTC())
	if errors.Is(err, store.ErrCLITokenLimit) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "mint CLI token", err)
		return
	}
	s.logger.Info("CLI token minted", "team", string(p.Team), "prefix", tok.Prefix, "by", p.AccountID)
	profile := string(p.Team)
	resp := cliTokenResp{
		Token: raw, Prefix: tok.Prefix, Scopes: tok.Scopes, Profile: profile,
		Setup: "sparkwing cloud connect --controller " + s.controllerURL(r) + " --name " + profile + " --token-stdin",
		Run:   "sparkwing pipeline trigger <pipeline> --profile " + profile,
	}
	if tok.ExpiresAt != nil {
		resp.ExpiresAt = tok.ExpiresAt.Unix()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleListCLITokens(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	toks, err := t.CLITokens(r.Context(), p.AccountID, time.Now())
	if err != nil {
		s.writeInternalError(w, r, "list CLI tokens", err)
		return
	}
	out := make([]cliTokenJSON, 0, len(toks))
	for _, tok := range toks {
		row := cliTokenJSON{Prefix: tok.Prefix, Scopes: tok.Scopes, CreatedAt: tok.CreatedAt.Unix()}
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

func (s *Server) handleRevokeCLIToken(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	prefix := r.PathValue("prefix")
	if err := t.RevokeCLIToken(r.Context(), prefix, p.AccountID, time.Now()); err != nil {
		writeIdentityError(w, s, r, "revoke CLI token", err)
		return
	}
	s.auth.Invalidate(prefix)
	w.WriteHeader(http.StatusNoContent)
}
