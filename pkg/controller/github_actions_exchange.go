package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type actionsProofKey struct{}

type actionsProof struct {
	spec       capability.ProvisionSpec
	repository string
}

// WithGitHubRunnerBindingLookup supplies the persisted repository binding reader.
func (s *Server) WithGitHubRunnerBindingLookup(lookup func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error)) *Server {
	s.githubRunners.lookup = lookup
	return s
}

func (s *Server) actionsEndpoints() []Endpoint {
	return []Endpoint{{
		Method: "POST", Pattern: "/api/v1/runners/github/exchange", Authentication: capability.AuthGitHubActions,
		Verify: s.verifyActionsExchange, Handle: s.issueActionsCredential,
	}}
}

func (s *Server) verifyActionsExchange(r *http.Request) error {
	verifier := s.githubVerifier()
	if verifier == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "GitHub Actions verification is not configured"}
	}
	var request githubExchangeReq
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	claims, err := verifier.Verify(r.Context(), request.IDToken)
	if errors.Is(err, githuboidc.ErrRejected) {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "GitHub Actions identity refused"}
	}
	if err != nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "GitHub Actions signing keys are unavailable"}
	}
	team := store.NormalizeTeam(store.Team(request.Team))
	branch, isBranch := strings.CutPrefix(claims.Ref, "refs/heads/")
	if team == "" || !githubRunnerEvents[claims.EventName] || !isBranch || branch == "" || !githubCommit(claims.SHA) || claims.RunID == "" || claims.WorkflowRef == "" {
		return actionsExchangeRefusal()
	}
	spec, err := s.actionsProvision(r.Context(), team, claims)
	if err != nil {
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), actionsProofKey{}, actionsProof{spec: spec, repository: claims.Repository}))
	return nil
}

func (s *Server) readActionsBinding(ctx context.Context, team store.Team, repositoryID int64) (store.GitHubRunnerBinding, error) {
	if s.githubRunners.lookup != nil {
		return s.githubRunners.lookup(ctx, team, repositoryID)
	}
	if s.store == nil {
		return store.GitHubRunnerBinding{}, missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(ctx, team)
	if err != nil {
		return store.GitHubRunnerBinding{}, err
	}
	return tenant.GitHubRunnerBinding(ctx, repositoryID)
}

func (s *Server) actionsProvision(ctx context.Context, team store.Team, claims githuboidc.Claims) (capability.ProvisionSpec, error) {
	binding, err := s.readActionsBinding(ctx, team, claims.RepositoryID)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	if err != nil {
		return capability.ProvisionSpec{}, engineLookupError(err)
	}
	if binding.Team != team || binding.RepositoryID != claims.RepositoryID || binding.RepositoryOwnerID != claims.RepositoryOwnerID {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{GitHubRepositoryID: runspec.Optional[int64]{Set: true, Value: claims.RepositoryID}})
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	expires := time.Now().UTC().Add(githubRunnerCredentialTTL)
	registration := "github:" + strconv.FormatInt(claims.RepositoryID, 10) + ":" + strconv.FormatInt(claims.RepositoryOwnerID, 10)
	return capability.ProvisionSpec{
		Kind: capability.ProvisionActions, Team: string(team), RegistrationID: registration,
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: registration + ":" + claims.RunID},
		Grants:  []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, ExpiresAt: &expires,
		Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{
			RepositoryKey: string(repository.Key), Ref: claims.Ref, SHA: claims.SHA, WorkflowRunID: claims.RunID,
		}}, Repositories: []string{string(repository.Key)},
	}, nil
}

func (s *Server) issueActionsCredential(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(actionsProofKey{}).(actionsProof)
	if !ok || s.credentialIssuer == nil {
		return missingEndpointPolicy()
	}
	issued, err := s.credentialIssuer.ProvisionCapability(r.Context(), proof.spec)
	if err != nil {
		return engineLookupError(err)
	}
	if issued.Capability.ExpiresAt == nil {
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "Actions credential has no expiry"}
	}
	writeJSON(w, http.StatusCreated, githubExchangeResp{
		Token: issued.Raw, Team: proof.spec.Team, Repository: proof.repository,
		ExpiresAt: issued.Capability.ExpiresAt.Unix(), Labels: []string{GitHubActionsLabel},
	})
	return nil
}

func actionsExchangeRefusal() error {
	return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "GitHub Actions repository binding refused")
}
