package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a plan claim builds the pipeline and a work claim runs it, so both fetch source.
var claimSourceKinds = []store.ClaimTokenKind{store.ClaimTokenPlan, store.ClaimTokenWork}

// safety: the claim token names its team and run, so the repository and
// commit come from the run's own trigger and never from the request.
func (s *Server) claimRunSource(w http.ResponseWriter, r *http.Request) (claimedRunSource, store.ClaimToken, bool) {
	tok, _ := claimTokenFromContext(r.Context())
	tenant, err := s.tenantForTeam(r.Context(), tok.Team)
	if err != nil {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	trigger, err := s.store.GetTrigger(r.Context(), tok.RunID)
	if err != nil || store.NormalizeTeam(trigger.Team) != tok.Team {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	return claimedRunSource{claimed: store.ClaimedRun{Team: tok.Team}, tenant: tenant, trigger: trigger}, tok, true
}

// safety: the init container and the user container carry the same claim
// token, so a claim is issued one token and none once its attempt has started;
// the store records the issue in the same transaction that checks both. The
// token reads only the run's repository and the ones its owner listed.
func (s *Server) handleRunSourceCredential(w http.ResponseWriter, r *http.Request) {
	src, tok, ok := s.claimRunSource(w, r)
	if !ok {
		return
	}
	t := src.trigger
	repo, onGitHub := runGitHubRepo(t)
	if !onGitHub || s.githubApp == nil {
		writeNoSourceCredential(w, "Sparkwing Cloud fetches source only through the team's GitHub App, "+
			"and run "+t.ID+" names no repository it covers; stored git credentials serve self-hosted runners only")
		return
	}
	if t.GitSHA == "" {
		writeError(w, http.StatusConflict, errors.New("run "+t.ID+" records no commit to check out"))
		return
	}
	extra, err := ownerExtraRepos(r.Context(), src.tenant, repo)
	if err != nil {
		s.writeInternalError(w, r, "read extra repositories", err)
		return
	}
	p, _ := PrincipalFromContext(r.Context())
	spec, err := src.tenant.SpendSourceCredential(r.Context(), tok, gitCredentialGitHubApp, "github.com", p.label(), time.Now())
	switch {
	case errors.Is(err, store.ErrSourceCredentialSpent):
		writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "source_credential_spent", Message: err.Error()})
		return
	case errors.Is(err, store.ErrClaimNotLive), errors.Is(err, store.ErrClaimCancelRequested),
		errors.Is(err, store.ErrClaimTokenInvalid):
		writeClaimRefusal(w, r, s, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "spend source credential", err)
		return
	}
	minted, failure := s.runAppToken(r, src, repo, extra)
	if failure != nil {
		failure.write(w)
		return
	}
	writeJSON(w, http.StatusOK, store.SourceCredential{
		Token: minted.Token, ExpiresAt: minted.ExpiresAt, RepoURL: "https://github.com/" + repo.Slug() + ".git",
		SHA: t.GitSHA, Branch: t.GitBranch, Repositories: append([]string{repo.Slug()}, minted.ExtraRepositories...),
		Source: spec,
	})
}
