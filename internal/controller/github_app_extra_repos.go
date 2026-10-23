package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// safety: Only the team owner may expand the App token beyond its run repository.
type githubAppExtraReposJSON struct {
	Repository string   `json:"repository"`
	ExtraRepos []string `json:"extra_repos"`
}

func (s *Server) handleListGitHubAppExtraRepos(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.githubApp == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errNoGitHubApp.Error()}
	}
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	all, err := t.AllGitHubAppExtraRepos(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := []githubAppExtraReposJSON{}
	for repo, extras := range all {
		out = append(out, githubAppExtraReposJSON{Repository: repo, ExtraRepos: extras})
	}
	slices.SortFunc(out, func(a, b githubAppExtraReposJSON) int { return strings.Compare(a.Repository, b.Repository) })
	writeJSON(w, http.StatusOK, map[string]any{"extra_repos": out})
	return nil
}

// safety: only a team owner widens what a run's token reads, never the
// repository's own code, and each listed repository must be covered by the
// same installation of the team's that covers the source repository, since
// that installation mints the token. The mint checks the coverage again.
func (s *Server) handlePutGitHubAppExtraRepos(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.githubApp == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errNoGitHubApp.Error()}
	}
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	var req githubAppExtraReposJSON
	if err := decodeEngineRequest(r, &req); err != nil {
		return githubAppRequestError(http.StatusBadRequest, err)
	}
	repo, ok := store.ParseGitHubRepo(req.Repository)
	if !ok {
		return githubAppRequestError(http.StatusBadRequest, errors.New("repository must be owner/name"))
	}
	if len(req.ExtraRepos) > store.MaxGitHubAppExtraRepos {
		return githubAppRequestError(http.StatusBadRequest, fmt.Errorf("at most %d extra repositories per repository", store.MaxGitHubAppExtraRepos))
	}
	ids, status, err := s.extraReposCovered(r.Context(), t, repo, req.ExtraRepos)
	if err != nil {
		return githubAppRequestError(status, err)
	}
	writer, err := s.githubAppWriter(r, a)
	if err != nil {
		return err
	}
	saved, err := writer.SetAuthorizedGitHubExtraRepos(r.Context(), a, repo.Slug(), req.ExtraRepos, ids)
	if errors.Is(err, store.ErrInvalidInput) {
		return githubAppRequestError(http.StatusBadRequest, err)
	}
	if err != nil {
		return engineLookupError(err)
	}
	s.logger.Info("github app extra repos written", "team", string(store.Team(a.Target.Team)), "repository", repo.Slug(),
		"extra_repos", strings.Join(saved, ","), "by", a.Principal.Capability.Subject.ID)
	writeJSON(w, http.StatusOK, githubAppExtraReposJSON{Repository: strings.ToLower(repo.Slug()), ExtraRepos: saved})
	return nil
}

// safety: One held installation must cover both the run repository and every extra repository,
// and each extra's GitHub ID is read from that installation's own list, so the approval binds the repository
// itself rather than whatever later holds its name.
func (s *Server) extraReposCovered(ctx context.Context, t *store.Tenant, repo store.GitHubRepo, extras []string) (map[string]int64, int, error) {
	inst, found, err := s.teamInstallationFor(ctx, t, repo)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("GitHub installation lookup: %w", err)
	}
	if !found {
		return nil, http.StatusNotFound, errors.New("no installation this team holds covers " + repo.Slug())
	}
	for _, raw := range extras {
		x, ok := store.ParseGitHubRepo(raw)
		if !ok {
			return nil, http.StatusBadRequest, errors.New("extra repository " + raw + " is not owner/name")
		}
		xinst, covered, err := s.teamInstallationFor(ctx, t, x)
		if err != nil {
			return nil, http.StatusBadGateway, fmt.Errorf("GitHub installation lookup for %s: %w", x.Slug(), err)
		}
		if !covered || xinst.InstallationID != inst.InstallationID {
			return nil, http.StatusBadRequest, errors.New("the installation covering " + repo.Slug() + " does not cover " + x.Slug())
		}
	}
	listed, err := s.githubApp.client.InstallationRepositories(ctx, inst.InstallationID)
	truncated := errors.Is(err, githubapp.ErrTooManyRepositories)
	if err != nil && !truncated {
		return nil, http.StatusBadGateway, fmt.Errorf("GitHub installation repositories: %w", err)
	}
	ids := map[string]int64{}
	for _, raw := range append([]string{repo.Slug()}, extras...) {
		for _, l := range listed {
			if strings.EqualFold(l.FullName, raw) {
				ids[strings.ToLower(raw)] = l.ID
			}
		}
		if ids[strings.ToLower(raw)] == 0 && truncated {
			return nil, http.StatusBadGateway, errors.New("the installation covers more repositories than GitHub lists, so " + raw + " cannot be confirmed")
		}
		if ids[strings.ToLower(raw)] == 0 {
			return nil, http.StatusBadRequest, errors.New("the installation covering " + repo.Slug() + " does not list " + raw)
		}
	}
	return ids, 0, nil
}
