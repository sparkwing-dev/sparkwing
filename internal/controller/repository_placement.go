package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// safety: placement comes from the default-branch head, read once per delivery, never the event's own
// revision, so a branch or pull request cannot add a paid tier.
func (s *Server) defaultBranchPlacement(installationID int64, repo store.GitHubRepo) func(context.Context, string) (*runspec.RepositoryPlacement, error) {
	var once sync.Once
	var cfg *projectconfig.Config
	var revision string
	var readErr error
	return func(ctx context.Context, pipeline string) (*runspec.RepositoryPlacement, error) {
		once.Do(func() {
			snapshot, err := s.githubApp.client.FileAtDefaultHead(ctx, installationID, repo.ID, repo.Owner, repo.Name, ".sparkwing/"+projectconfig.Filename)
			// safety: a default branch with no head has no verified configuration, so it has no placement.
			switch {
			case errors.Is(err, githubapp.ErrRefNotFound):
			case err != nil:
				readErr = fmt.Errorf("read default-branch placement: %w", err)
			case snapshot.RepositoryID != repo.ID:
				readErr = errors.New("GitHub repository identity changed while reading placement")
			case snapshot.Found:
				revision = snapshot.HeadSHA
				if cfg, err = projectconfig.Parse(snapshot.Content); err != nil {
					readErr = &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("default-branch .sparkwing/%s at %s: %v", projectconfig.Filename, snapshot.HeadSHA, err)}
				}
			}
		})
		if readErr != nil {
			return nil, readErr
		}
		return cfg.RepositoryPlacement(pipeline, revision)
	}
}

// safety: only a top-level run on a commit the controller fetched through the team's installation reads its
// default-branch list, even when a compare outage leaves it unverified. A child takes its parent job's sealed list
// at intake and reads none; burst runs read none.
func (s *Server) requestPlacement(ctx context.Context, team store.Team, request runspec.Request, fetched store.FetchedRepository) (*runspec.RepositoryPlacement, error) {
	if s.githubApp == nil || request.Burst != nil || request.Lineage.ParentRunID != "" || fetched.ID == 0 {
		return nil, nil
	}
	access := &intakeRevisionAccess{server: s, team: team}
	installation, repository, err := access.githubAppRepository(ctx, source.Spec{RepoKey: string(request.Git.Repository.Key), RemoteURL: request.Source.Location.RemoteURL})
	if err != nil {
		return nil, err
	}
	repo, ok := store.ParseGitHubRepo(repository.FullName)
	repo.ID = repository.ID
	if !ok || repo.ID != fetched.ID {
		return nil, errors.New("GitHub repository identity changed while reading placement")
	}
	return s.defaultBranchPlacement(installation.InstallationID, repo)(ctx, request.Pipeline)
}
