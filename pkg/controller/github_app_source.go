package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SourceTokenResponse is a short-lived GitHub credential that reads one
// repository's contents, for the runner holding a claim on a run of that
// repository. The runner hands it to git in the fetch's environment only.
type SourceTokenResponse struct {
	Token      string `json:"token"`
	ExpiresAt  int64  `json:"expires_at"`
	Repository string `json:"repository"`
}

func runGitHubRepo(trigger *store.Trigger) (store.GitHubRepo, bool) {
	identity, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil || identity == "" {
		return store.GitHubRepo{}, false
	}
	slug, ok := strings.CutPrefix(identity, "github.com/")
	if !ok {
		return store.GitHubRepo{}, false
	}
	return store.ParseGitHubRepo(slug)
}

// safety: the token reads only the run's own repository, only a runner holding
// live work on the run gets one, and only when an installation the run's team
// holds covers that repository, so naming a repository in a run proves nothing
// by itself.
func (s *Server) handleRunSourceToken(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	runID := r.PathValue("id")
	p, _ := PrincipalFromContext(r.Context())
	claimed, err := s.store.ClaimedRunFor(r.Context(), runID, claimIdentity(r), time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " is not claimed by this principal",
		})
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "source token claim", err)
		return
	}
	tenant, err := s.tenantForTeam(r.Context(), claimed.Team)
	if err != nil {
		s.writeInternalError(w, r, "source token team", err)
		return
	}
	trigger, err := s.store.GetTrigger(r.Context(), runID)
	if err != nil || store.NormalizeTeam(trigger.Team) != claimed.Team {
		writeError(w, http.StatusNotFound, runNotFound(runID))
		return
	}
	repo, ok := runGitHubRepo(trigger)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("run "+runID+" names no GitHub repository"))
		return
	}
	inst, found, err := s.teamInstallationFor(r.Context(), tenant, repo)
	if err != nil {
		s.logger.Warn("source token installation", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation"))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no GitHub App installation this team holds covers "+repo.Slug()))
		return
	}
	tok, err := s.githubApp.client.InstallationToken(r.Context(), inst.InstallationID, repo.Name,
		map[string]string{"contents": "read"})
	if errors.Is(err, githubapp.ErrNotInstalled) {
		writeError(w, http.StatusNotFound, errors.New("no GitHub App installation this team holds covers "+repo.Slug()))
		return
	}
	if err != nil {
		s.logger.Warn("source token mint", "run_id", runID, "repository", repo.Slug(), "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub did not issue a token for "+repo.Slug()))
		return
	}
	s.logger.Info("source token minted", "team", string(claimed.Team), "run_id", runID,
		"repository", repo.Slug(), "installation_id", inst.InstallationID, "principal", p.label())
	writeJSON(w, http.StatusOK, SourceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix(), Repository: repo.Slug()})
}

func (s *Server) runUntrusted(ctx context.Context, team store.Team, runID string) (bool, error) {
	t, err := s.tenantForTeam(ctx, team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	untrusted, err := t.RunUntrusted(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return untrusted, err
}
