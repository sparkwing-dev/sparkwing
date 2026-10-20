package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// GitHubRunnerRegistry commits repository enrollment and root revocation with live authority checks.
type GitHubRunnerRegistry interface {
	Team() store.Team
	GitHubRunnerBindings(context.Context) ([]store.GitHubRunnerBinding, error)
	RegisterGitHubRunner(context.Context, capability.Authorized, store.GitHubRunnerBinding) (store.GitHubRunnerBinding, error)
	RevokeGitHubRunner(context.Context, capability.Authorized, int64) error
}

func (s *Server) githubRunnerRegistry(ctx context.Context, team string) (GitHubRunnerRegistry, error) {
	return tenantPort[GitHubRunnerRegistry](ctx, s, store.Team(team))
}

func (s *Server) githubRunnerEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/runners/github", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListGitHubRunnerBindings},
		{Method: "POST", Pattern: "/api/v1/runners/github", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleAddGitHubRunnerBinding},
		{Method: "DELETE", Pattern: "/api/v1/runners/github/{repository}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRemoveGitHubRunnerBinding},
	}
}

func (s *Server) readGitHubRunnerBindings(ctx context.Context, team string) ([]store.GitHubRunnerBinding, error) {
	registry, err := s.githubRunnerRegistry(ctx, team)
	if err != nil {
		return nil, err
	}
	return registry.GitHubRunnerBindings(ctx)
}

func (s *Server) handleListGitHubRunnerBindings(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	bindings, err := s.readGitHubRunnerBindings(r.Context(), a.Target.Team)
	if err != nil {
		return engineLookupError(err)
	}
	origin, err := s.publicOrigin()
	if err != nil {
		return err
	}
	out := githubBindingsResp{Bindings: []githubBindingJSON{}, Workflow: GitHubRunnerWorkflow(origin, a.Target.Team)}
	for _, binding := range bindings {
		if !validGitHubRunnerBinding(binding, a.Target.Team) {
			return errors.New("invalid repository enrollment")
		}
		out.Bindings = append(out.Bindings, githubBindingOut(binding))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func validGitHubRunnerBinding(binding store.GitHubRunnerBinding, team string) bool {
	_, valid := store.ParseGitHubRepo(binding.Repository)
	return valid && binding.Team == store.Team(team) && binding.RepositoryID > 0 && binding.RepositoryOwnerID > 0
}

func (s *Server) handleAddGitHubRunnerBinding(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	origin, err := s.publicOrigin()
	if err != nil {
		return err
	}
	var request githubBindingReq
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if _, valid := store.ParseGitHubRepo(request.Repository); !valid || request.RepositoryID < 0 || request.RepositoryOwnerID < 0 {
		return invalidEngineBody("repository must be owner/name with positive numeric identities")
	}
	registry, err := s.githubRunnerRegistry(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := s.resolveGitHubRunnerIdentity(r.Context(), a.Target.Team, &request); err != nil {
		return err
	}
	if request.RepositoryID <= 0 || request.RepositoryOwnerID <= 0 {
		return invalidEngineBody("repository_id and repository_owner_id must be positive")
	}
	binding, err := registry.RegisterGitHubRunner(r.Context(), a, store.GitHubRunnerBinding{
		Team: store.Team(a.Target.Team), Repository: request.Repository, RepositoryID: request.RepositoryID,
		RepositoryOwnerID: request.RepositoryOwnerID, CreatedBy: a.Principal.Capability.Subject.ID,
	})
	if errors.Is(err, store.ErrAlreadyBound) {
		return &apierr.Error{Code: apierr.Conflict, Message: "repository is already enrolled"}
	}
	if err != nil {
		return engineLookupError(err)
	}
	if !validGitHubRunnerBinding(binding, a.Target.Team) || binding.RepositoryID != request.RepositoryID || binding.RepositoryOwnerID != request.RepositoryOwnerID {
		return errors.New("invalid repository enrollment")
	}
	writeJSON(w, http.StatusCreated, githubBindingsResp{Bindings: []githubBindingJSON{githubBindingOut(binding)}, Workflow: GitHubRunnerWorkflow(origin, a.Target.Team)})
	return nil
}

func (s *Server) resolveGitHubRunnerIdentity(ctx context.Context, team string, request *githubBindingReq) error {
	if s.githubApp == nil {
		return nil
	}
	if s.operator == nil {
		return missingEndpointPolicy()
	}
	tenant, err := s.tenantForTeam(ctx, store.Team(team))
	if err != nil {
		return engineLookupError(err)
	}
	_, repo, err := s.resolveGitHubAppRepository(ctx, tenant, request.Repository)
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		return invalidEngineBody("repository identity is invalid")
	case errors.Is(err, store.ErrNotFound):
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "the team's GitHub App installation does not cover this repository")
	case err != nil:
		return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "GitHub repository coverage could not be verified"}, err)
	}
	if request.RepositoryID != 0 && request.RepositoryID != repo.ID || request.RepositoryOwnerID != 0 && request.RepositoryOwnerID != repo.Owner.ID {
		return invalidEngineBody("repository identity differs from GitHub")
	}
	request.Repository, request.RepositoryID, request.RepositoryOwnerID = repo.FullName, repo.ID, repo.Owner.ID
	return nil
}

func (s *Server) handleRemoveGitHubRunnerBinding(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	id, err := strconv.ParseInt(r.PathValue("repository"), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != r.PathValue("repository") {
		return invalidEngineBody("repository must be GitHub's positive numeric repository id")
	}
	registry, err := s.githubRunnerRegistry(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := registry.RevokeGitHubRunner(r.Context(), a, id); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
