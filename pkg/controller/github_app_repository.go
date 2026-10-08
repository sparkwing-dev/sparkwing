package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) resolveGitHubAppRepository(ctx context.Context, tenant *store.Tenant, slug string) (store.GitHubAppInstallation, githubapp.Repository, error) {
	repo, ok := store.ParseGitHubRepo(slug)
	if !ok {
		return store.GitHubAppInstallation{}, githubapp.Repository{}, fmt.Errorf("%w: repository must be owner/name", store.ErrInvalidInput)
	}
	in, found, err := s.teamInstallationFor(ctx, tenant, repo)
	if err != nil {
		return in, githubapp.Repository{}, err
	}
	if !found {
		return in, githubapp.Repository{}, store.ErrNotFound
	}
	repositories, err := s.githubApp.client.InstallationRepositories(ctx, in.InstallationID)
	if err != nil {
		return in, githubapp.Repository{}, err
	}
	for _, current := range repositories {
		if !strings.EqualFold(current.FullName, repo.Slug()) {
			continue
		}
		canonical, valid := store.ParseGitHubRepo(current.FullName)
		if !valid || current.ID <= 0 || current.Owner.ID <= 0 || current.Owner.ID != in.AccountID ||
			!strings.EqualFold(current.Owner.Login, canonical.Owner) || !strings.EqualFold(in.AccountLogin, canonical.Owner) {
			return in, githubapp.Repository{}, fmt.Errorf("%w: GitHub repository ownership changed; reconnect the installation", store.ErrInvalidInput)
		}
		current.FullName = canonical.Slug()
		return in, current, nil
	}
	return in, githubapp.Repository{}, store.ErrNotFound
}
