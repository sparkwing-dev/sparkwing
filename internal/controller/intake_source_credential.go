package controller

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) verifyIntakeSourceCredential(ctx context.Context, team store.Team, request runspec.Request) (store.FetchedRepository, bool, error) {
	here := request.Options.Runner.Set && request.Options.Runner.Value.Kind == runspec.RunnerHere
	if s.githubApp == nil || !request.Source.FetchedFromRemote() || here || request.Lineage.ParentRunID != "" || request.Lineage.RetryOf != "" {
		return store.FetchedRepository{}, false, nil
	}
	key, github := strings.CutPrefix(string(request.Git.Repository.Key), "gh:")
	if !github {
		return store.FetchedRepository{}, false, nil
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id <= 0 {
		return store.FetchedRepository{}, false, &apierr.Error{Code: apierr.BadRequest, Message: "GitHub source repository requires a positive numeric ID"}
	}
	access := &intakeRevisionAccess{server: s, team: team}
	spec := source.Spec{RepoKey: string(request.Git.Repository.Key), RemoteURL: request.Source.Location.RemoteURL}
	installation, repository, err := access.githubAppRepository(ctx, spec)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, githubapp.ErrNotInstalled) || errors.Is(err, errUncoveredGitHubRepository) {
		return store.FetchedRepository{}, false, nil
	}
	if err != nil {
		return store.FetchedRepository{}, false, err
	}
	repo, _ := store.ParseGitHubRepo(repository.FullName)
	ref := request.Git.Ref
	if pin := request.Source.RequestedRef; pin != "" && !runspec.IsCommitID(pin) {
		ref = pin
	}
	for _, prefix := range []string{"origin/", "refs/remotes/origin/"} {
		if branch, ok := strings.CutPrefix(ref, prefix); ok {
			ref = "refs/heads/" + branch
		}
	}
	if pr := request.PullRequest; pr.Set && !pr.Value.Fork && pr.Value.HeadRepo == request.Git.Repository.Key && pr.Value.HeadRef != "" && (request.Source.RequestedRef == "" || runspec.IsCommitID(request.Source.RequestedRef)) {
		ref = "refs/heads/" + runspec.NormalizedBranch(pr.Value.HeadRef)
	}
	if ref == "" || ref == "HEAD" || runspec.IsCommitID(ref) || strings.HasPrefix(ref, "refs/") && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		if repository.DefaultBranch == "" {
			token, err := s.githubApp.client.InstallationTokenByIDs(ctx, installation.InstallationID, []int64{id}, map[string]string{"metadata": "read"})
			if err != nil {
				return store.FetchedRepository{}, false, err
			}
			repository, err = s.githubApp.client.RepositoryByID(ctx, token.Token, id)
			if err != nil {
				return store.FetchedRepository{}, false, err
			}
			if repository.ID != id {
				return store.FetchedRepository{}, false, errors.New("GitHub source repository identity changed")
			}
		}
		if repository.DefaultBranch == "" {
			return store.FetchedRepository{}, false, nil
		}
		ref = "refs/heads/" + repository.DefaultBranch
	} else if !strings.HasPrefix(ref, "refs/") {
		selection := request.Git
		selection.Ref, selection.SHA = ref, request.Source.Ref
		git, err := qualifyFetchedRef(ctx, selection, access.resolveGitHubAppRef(spec))
		if err != nil {
			return store.FetchedRepository{}, false, err
		}
		ref = git.Ref
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + runspec.NormalizedBranch(ref)
		}
	}
	if !runspec.IsCommitID(request.Source.Ref) {
		return store.FetchedRepository{}, false, nil
	}
	verified, err := s.githubApp.client.CommitReachable(ctx, installation.InstallationID, id, repo.Owner, repo.Name, request.Source.Ref, ref)
	if err != nil || !verified {
		return store.FetchedRepository{}, false, err
	}
	// A caller's selected commit becomes repository proof only after the App
	// selects its configuration and code, and GitHub confirms reachability.
	git, selected, err := s.appSourceSelector(team)(ctx, request)
	if err != nil {
		return store.FetchedRepository{}, false, err
	}
	if !strings.EqualFold(git.SHA, request.Git.SHA) || !strings.EqualFold(selected.Ref, request.Source.Ref) {
		return store.FetchedRepository{}, false, nil
	}
	return store.FetchedRepository{ID: id, Name: repository.FullName}, true, nil
}
