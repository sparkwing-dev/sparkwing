package controller

import (
	"errors"
	"net/http"
	"strings"
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
	trigger, err := tenant.GetTrigger(r.Context(), tok.RunID)
	if err != nil || store.NormalizeTeam(trigger.Team) != tok.Team {
		writeError(w, http.StatusNotFound, runNotFound(tok.RunID))
		return claimedRunSource{}, tok, false
	}
	return claimedRunSource{claimed: store.ClaimedRun{Team: tok.Team}, tenant: tenant, trigger: trigger}, tok, true
}

// safety: the user container holds the init container's claim token, so a claim gets a few tokens and none
// once its attempt starts, checked where the issue is recorded. A token reads the run's repository and the
// listed ones by GitHub ID, so a rename or a reused name widens nothing.
func (s *Server) handleRunSourceCredential(w http.ResponseWriter, r *http.Request) {
	src, tok, ok := s.claimRunSource(w, r)
	if !ok {
		return
	}
	t := src.trigger
	repo, onGitHub := runGitHubRepo(t)
	if !onGitHub || s.githubApp == nil {
		writeNoSourceCredential(w, cloudSourceNeedsApp(t.ID))
		return
	}
	if t.GitSHA == "" {
		writeError(w, http.StatusConflict, errors.New("run "+t.ID+" records no commit to check out"))
		return
	}
	ctx := r.Context()
	extra, err := src.tenant.GitHubAppExtraRepoRefs(ctx, repo.Slug())
	if err != nil {
		s.writeInternalError(w, r, "read extra repositories", err)
		return
	}
	inst, covered, err := s.teamInstallationFor(ctx, src.tenant, repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation"))
		return
	}
	if !covered {
		writeNoSourceCredential(w, cloudSourceNeedsApp(t.ID))
		return
	}
	for _, x := range extra {
		xinst, xcovered, err := s.teamInstallationFor(ctx, src.tenant, x)
		if err != nil || !xcovered || xinst.InstallationID != inst.InstallationID || x.ID == 0 {
			writeError(w, http.StatusConflict, errors.New("extra repository "+x.Slug()+
				" is not covered by the installation covering "+repo.Slug()+" or has no recorded id; "+
				"a team owner saves the list again (Team > GitHub)"))
			return
		}
	}
	p, _ := PrincipalFromContext(ctx)
	spend, err := src.tenant.SpendSourceCredential(ctx, tok, gitCredentialGitHubApp, "github.com", p.label(), time.Now())
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
	if spend.RepoID == 0 {
		writeError(w, http.StatusConflict, errors.New("run "+t.ID+" recorded no GitHub repository id; "+
			"Sparkwing Cloud fetches only runs its GitHub App started"))
		return
	}
	ids, slugs := []int64{spend.RepoID}, []string{repo.Slug()}
	for _, x := range extra {
		ids, slugs = append(ids, x.ID), append(slugs, x.Slug())
	}
	minted, err := s.githubApp.client.InstallationTokenByIDs(ctx, inst.InstallationID, ids, map[string]string{"contents": "read"})
	if err != nil {
		s.logger.Warn("source credential mint", "run_id", t.ID, "repository", repo.Slug(), "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub did not issue a token for "+repo.Slug()+"; ask again"))
		return
	}
	s.logger.Info("source credential minted", "team", string(tok.Team), "run_id", t.ID, "node_id", tok.NodeID,
		"repositories", strings.Join(slugs, ","), "installation_id", inst.InstallationID)
	writeJSON(w, http.StatusOK, store.SourceCredential{
		Token: minted.Token, ExpiresAt: minted.ExpiresAt.Unix(), RepoURL: "https://github.com/" + repo.Slug() + ".git",
		SHA: t.GitSHA, Branch: t.GitBranch, Repositories: slugs, Source: spend.Spec,
	})
}

func cloudSourceNeedsApp(runID string) string {
	return "Sparkwing Cloud fetches source only through the team's GitHub App, and no installation of the team's " +
		"covers run " + runID + "'s repository (Team > GitHub); stored git credentials serve self-hosted runners only"
}
