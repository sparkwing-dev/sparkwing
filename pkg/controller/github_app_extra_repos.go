package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// githubAppExtraReposJSON is a team owner's list of the further repositories
// a run of Repository's App token also reads.
type githubAppExtraReposJSON struct {
	Repository string   `json:"repository"`
	ExtraRepos []string `json:"extra_repos"`
}

func (s *Server) handleListGitHubAppExtraRepos(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	all, err := t.AllGitHubAppExtraRepos(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list github app extra repos", err)
		return
	}
	out := []githubAppExtraReposJSON{}
	for repo, extras := range all {
		out = append(out, githubAppExtraReposJSON{Repository: repo, ExtraRepos: extras})
	}
	slices.SortFunc(out, func(a, b githubAppExtraReposJSON) int { return strings.Compare(a.Repository, b.Repository) })
	writeJSON(w, http.StatusOK, map[string]any{"extra_repos": out})
}

// safety: only a team owner widens what a run's token reads, never the
// repository's own code, and each listed repository must be covered by the
// same installation of the team's that covers the source repository, since
// that installation mints the token. The mint checks the coverage again.
func (s *Server) handlePutGitHubAppExtraRepos(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req githubAppExtraReposJSON
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	repo, ok := store.ParseGitHubRepo(req.Repository)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("repository must be owner/name"))
		return
	}
	if len(req.ExtraRepos) > store.MaxGitHubAppExtraRepos {
		writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d extra repositories per repository", store.MaxGitHubAppExtraRepos))
		return
	}
	if len(req.ExtraRepos) > 0 {
		if status, err := s.extraReposCovered(r.Context(), t, repo, req.ExtraRepos); err != nil {
			writeError(w, status, err)
			return
		}
	}
	saved, err := t.SetGitHubAppExtraRepos(r.Context(), repo.Slug(), req.ExtraRepos, p.AccountID, time.Now())
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "put github app extra repos", err)
		return
	}
	s.logger.Info("github app extra repos written", "team", string(p.Team), "repository", repo.Slug(),
		"extra_repos", strings.Join(saved, ","), "by", p.AccountID)
	writeJSON(w, http.StatusOK, githubAppExtraReposJSON{Repository: strings.ToLower(repo.Slug()), ExtraRepos: saved})
}

// extraReposCovered checks that one installation the team holds covers repo
// and every one of extras, answering the status to refuse with otherwise.
func (s *Server) extraReposCovered(ctx context.Context, t *store.Tenant, repo store.GitHubRepo, extras []string) (int, error) {
	inst, found, err := s.teamInstallationFor(ctx, t, repo)
	if err != nil {
		s.logger.Warn("github app extra repos lookup", "repository", repo.Slug(), "err", err.Error())
		return http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation")
	}
	if !found {
		return http.StatusNotFound, errors.New("no installation this team holds covers " + repo.Slug())
	}
	for _, raw := range extras {
		x, ok := store.ParseGitHubRepo(raw)
		if !ok {
			return http.StatusBadRequest, errors.New("extra repository " + raw + " is not owner/name")
		}
		xinst, covered, err := s.teamInstallationFor(ctx, t, x)
		if err != nil {
			s.logger.Warn("github app extra repos lookup", "repository", x.Slug(), "err", err.Error())
			return http.StatusBadGateway, errors.New("GitHub could not be reached to find the installation of " + x.Slug())
		}
		if !covered || xinst.InstallationID != inst.InstallationID {
			return http.StatusBadRequest, errors.New("the installation covering " + repo.Slug() + " does not cover " + x.Slug())
		}
	}
	return 0, nil
}

// ownerExtraRepos is the list a team owner set for repo, parsed.
func ownerExtraRepos(ctx context.Context, t *store.Tenant, repo store.GitHubRepo) ([]store.GitHubRepo, error) {
	slugs, err := t.GitHubAppExtraRepos(ctx, repo.Slug())
	if err != nil {
		return nil, err
	}
	var out []store.GitHubRepo
	for _, slug := range slugs {
		if x, ok := store.ParseGitHubRepo(slug); ok {
			out = append(out, x)
		}
	}
	return out, nil
}

func repoSlugs(repos []store.GitHubRepo) []string {
	var out []string
	for _, x := range repos {
		out = append(out, x.Slug())
	}
	return out
}
