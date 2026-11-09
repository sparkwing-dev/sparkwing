package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func (s *Server) actionsToken(ctx context.Context, d store.ActionsDispatch, actions string) (githubapp.Token, githubapp.Repository, error) {
	if s.githubApp == nil {
		return githubapp.Token{}, githubapp.Repository{}, s.githubAppUnavailable()
	}
	inst, err := s.githubApp.client.Installation(ctx, d.InstallationID)
	if err != nil {
		return githubapp.Token{}, githubapp.Repository{}, err
	}
	if inst.ID != d.InstallationID || inst.AppID != s.githubApp.client.AppID() || inst.Account.ID != d.RepositoryOwnerID || inst.SuspendedAt != nil {
		return githubapp.Token{}, githubapp.Repository{}, actionsExchangeRefusal("App installation identity or suspension check failed; check the installed App, owner and suspension status")
	}
	permissions := map[string]string{"contents": "read"}
	if actions != "" {
		permissions["actions"] = actions
	}
	token, err := s.githubApp.client.InstallationTokenByIDs(ctx, d.InstallationID, []int64{d.RepositoryID}, permissions)
	if err != nil {
		return githubapp.Token{}, githubapp.Repository{}, err
	}
	repo, err := s.githubApp.client.RepositoryByID(ctx, token.Token, d.RepositoryID)
	if err != nil {
		return githubapp.Token{}, githubapp.Repository{}, err
	}
	if repo.ID != d.RepositoryID || repo.Owner.ID != d.RepositoryOwnerID || repo.FullName != d.Repository {
		return githubapp.Token{}, githubapp.Repository{}, actionsExchangeRefusal("repository identity verification failed; check the App installation and repository access")
	}
	return token, repo, nil
}

// safety: GitHub signs a workflow_dispatch OIDC token with the dispatched ref, so a fallback on
// the default branch or a tag would lend pull-request and branch code an identity that cloud
// roles commonly trust. Only a dedicated branch is dispatched.
func actionsFallbackRefProblem(ref string, repo githubapp.Repository) string {
	if !strings.HasPrefix(ref, "refs/heads/") || repo.DefaultBranch == "" || ref == "refs/heads/"+repo.DefaultBranch {
		return "approve a dedicated protected branch such as refs/heads/sparkwing-fallback as the fallback ref; the default branch and tags lend their OIDC identity to fallback jobs"
	}
	return ""
}

func (s *Server) actionsRepair(ctx context.Context, d store.ActionsDispatch, err error) string {
	if errors.Is(err, githubapp.ErrPermissionMissing) {
		return "approve Actions write for the GitHub App: " + s.actionsApprovalURL(ctx, d)
	}
	return "GitHub workflow unavailable; check the App installation, repository access and approved workflow"
}

func (s *Server) actionsWorkflowProblem(ctx context.Context, d store.ActionsDispatch, sha string) string {
	repo, ok := store.ParseGitHubRepo(d.Repository)
	if !ok {
		return "repository name is invalid; approve the fallback again with OWNER/REPO"
	}
	workflow, present, err := s.githubApp.client.FileAtCommit(ctx, d.InstallationID, d.RepositoryID, repo.Owner, repo.Name, sha, d.Workflow)
	if err != nil {
		return "fallback workflow read failed; check GitHub availability and App repository access, then retry"
	}
	if !present {
		return "fallback workflow is missing; add the approved workflow at the fallback ref"
	}
	if int64(len(workflow)) > limits.ProjectConfigBytes {
		return "fallback workflow exceeds the configuration size limit; reduce the workflow file"
	}
	defaultWorkflow, err := s.githubApp.client.FileAtDefaultHead(ctx, d.InstallationID, d.RepositoryID, repo.Owner, repo.Name, d.Workflow)
	if err != nil {
		return "default-branch workflow read failed; check GitHub availability and App repository access, then retry"
	}
	if defaultWorkflow.RepositoryID != d.RepositoryID {
		return "default-branch repository identity changed; approve the fallback again for the correct repository"
	}
	if !defaultWorkflow.Found {
		return "default-branch workflow is missing; add the approved workflow on the default branch"
	}
	if int64(len(defaultWorkflow.Content)) > limits.ProjectConfigBytes {
		return "default-branch workflow exceeds the configuration size limit; reduce the workflow file"
	}
	if !bytes.Equal(workflow, defaultWorkflow.Content) {
		return "workflow contents differ; sync the fallback branch's workflow with the default branch"
	}
	return ""
}

func (s *Server) processActionsDispatch(ctx context.Context) (bool, error) {
	d, err := s.operator.NextActionsDispatch(ctx)
	if err != nil || d == nil {
		return false, err
	}
	token, repository, err := s.actionsToken(ctx, *d, "write")
	if err != nil {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", s.actionsRepair(ctx, *d, err))
	}
	if problem := actionsFallbackRefProblem(d.WorkflowRef, repository); problem != "" {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", problem)
	}
	repo, ok := store.ParseGitHubRepo(d.Repository)
	if !ok {
		return true, store.ErrInvalidInput
	}
	sha, err := s.githubApp.client.ResolveCommit(ctx, d.InstallationID, repo.Owner, repo.Name, d.WorkflowRef)
	if err != nil {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", "approved workflow ref cannot be resolved; check the branch or tag")
	}
	if problem := s.actionsWorkflowProblem(ctx, *d, sha); problem != "" {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", fmt.Sprintf("cannot verify %s: %s", d.Workflow, problem))
	}
	begun, err := s.operator.BeginActionsDispatch(ctx, *d, sha)
	if err != nil {
		return true, err
	}
	if !begun {
		return true, s.operator.FinishSkippedActionsDispatch(ctx, d.Receipt)
	}
	status, sendErr := s.githubApp.client.DispatchWorkflow(ctx, token.Token, d.Repository, d.Workflow, d.WorkflowRef, d.Receipt, d.Runner)
	state, reason := "accepted", ""
	switch {
	case sendErr != nil || status >= 500 || status == 0:
		state, reason = "uncertain", "dispatch outcome unknown; no automatic redispatch; an operator can retry a new generation (extra GitHub startup minutes may be spent)"
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		state, reason = "refused", "approve Actions write and check repository Actions settings: "+s.actionsApprovalURL(ctx, *d)
	case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		state, reason = "refused", "approved workflow missing, disabled or lacks workflow_dispatch; check repository Actions settings"
	case status == http.StatusTooManyRequests:
		state, reason = "refused", "GitHub rate limited the dispatch; an operator can retry a new generation"
	case status != http.StatusNoContent:
		state, reason = "refused", fmt.Sprintf("GitHub refused dispatch (HTTP %d); check repository Actions settings", status)
	}
	return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, state, reason)
}

// safety: each receipt fails alone, so one team's broken installation never holds back another team's cancellations.
func (s *Server) processPendingActionsCancellations(ctx context.Context) error {
	pending, err := s.operator.PendingActionsCancellations(ctx)
	if err != nil {
		return err
	}
	for _, d := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.cancelActionsRun(ctx, d)
		if err != nil && !actionsCancellationRefused(err) {
			s.logger.Warn("GitHub Actions cancellation will retry", "team", d.Team, "repository", d.Repository, "provider_run", d.ProviderRunID, "err", err)
			continue
		}
		if err != nil {
			s.logger.Warn("GitHub refused Actions cancellation; cancel the run in GitHub if it is still going", "team", d.Team, "repository", d.Repository, "provider_run", d.ProviderRunID, "err", err)
		}
		if err := s.operator.FinishActionsCancellation(ctx, d.Receipt); err != nil {
			s.logger.Warn("GitHub Actions cancellation not recorded", "team", d.Team, "receipt", d.Receipt, "err", err)
		}
	}
	return nil
}

func (s *Server) cancelActionsRun(ctx context.Context, d store.ActionsDispatch) error {
	token, _, err := s.actionsToken(ctx, d, "write")
	if err != nil {
		return err
	}
	return s.githubApp.client.CancelActionsRun(ctx, token.Token, d.Repository, d.ProviderRunID)
}

// safety: only answers a retry cannot change end retries: the App was removed, suspended
// or moved, or GitHub refused its credential or permission.
func actionsCancellationRefused(err error) bool {
	var refusal *apierr.Error
	return errors.Is(err, githubapp.ErrNotInstalled) || errors.Is(err, githubapp.ErrRejected) || errors.Is(err, githubapp.ErrPermissionMissing) || errors.As(err, &refusal)
}

func (s *Server) runActionsDispatcher(ctx context.Context) {
	if s.githubApp == nil || s.operator == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.processPendingActionsCancellations(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("GitHub Actions cancellation failed", "err", err)
		}
		for ctx.Err() == nil {
			sent, err := s.processActionsDispatch(ctx)
			if err != nil {
				s.logger.Warn("GitHub Actions dispatch failed", "err", err)
				break
			}
			if !sent {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) actionsReceiptProvision(ctx context.Context, team store.Team, receipt string, claims githuboidc.Claims) (capability.ProvisionSpec, error) {
	if s.operator == nil || !runspec.IsCommitID(claims.WorkflowSHA) || claims.EventName != "workflow_dispatch" || claims.RunnerEnvironment != "github-hosted" || claims.RunAttempt != "1" || claims.CheckRunID == "" {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("fallback OIDC claims are invalid; start a new dispatch on a GitHub-hosted runner")
	}
	d, err := s.operator.ReadActionsDispatch(ctx, team, receipt)
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("dispatch receipt lookup failed; check the team and receipt, then start a new dispatch")
	}
	if d.RepositoryID != claims.RepositoryID || d.RepositoryOwnerID != claims.RepositoryOwnerID || d.Repository != claims.Repository || claims.WorkflowRef != d.Repository+"/"+d.Workflow+"@"+d.WorkflowRef || claims.WorkflowSHA != d.WorkflowSHA || claims.SHA != d.WorkflowSHA || claims.Ref != d.WorkflowRef || !slices.Contains([]string{"sending", "accepted", "uncertain"}, d.State) {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("dispatch identity or workflow SHA does not match the receipt; start a new dispatch")
	}
	token, repository, err := s.actionsToken(ctx, d, "read")
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("App repository access check failed; check the installation and repository access, then retry")
	}
	if problem := actionsFallbackRefProblem(d.WorkflowRef, repository); problem != "" {
		return capability.ProvisionSpec{}, actionsExchangeRefusal(problem)
	}
	if problem := s.actionsWorkflowProblem(ctx, d, d.WorkflowSHA); problem != "" {
		return capability.ProvisionSpec{}, actionsExchangeRefusal(d.Workflow + ": " + problem)
	}
	run, err := s.githubApp.client.ActionsRun(ctx, token.Token, d.Repository, claims.RunID)
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("GitHub run lookup failed; check GitHub availability and App repository access, then retry")
	}
	if strconv.FormatInt(run.ID, 10) != claims.RunID || strconv.FormatInt(run.Attempt, 10) != claims.RunAttempt || run.Event != "workflow_dispatch" || run.Title != "sparkwing:"+receipt || run.Path != d.Workflow || run.SHA != d.WorkflowSHA || run.Repository.ID != d.RepositoryID || run.Repository.Owner.ID != d.RepositoryOwnerID {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("GitHub run identity does not match the dispatch; start a new dispatch")
	}
	labels, err := s.githubApp.client.ActionsJobLabels(ctx, token.Token, d.Repository, claims.RunID, claims.RunAttempt, claims.CheckRunID)
	if err != nil || !slices.Contains(labels, d.Runner) || slices.Contains(labels, "self-hosted") {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("GitHub job label verification failed; check App repository access and use the approved GitHub-hosted runner")
	}
	tenant, err := s.tenantForTeam(ctx, team)
	if err != nil {
		return capability.ProvisionSpec{}, err
	}
	workload, err := tenant.ReadRun(ctx, d.RunID)
	if err != nil {
		return capability.ProvisionSpec{}, err
	}
	if workload.Request.Request.PullRequest.Set && workload.Request.Request.PullRequest.Value.Fork {
		return capability.ProvisionSpec{}, actionsExchangeRefusal("fork pull-request fallback is refused; run the fallback from a branch in the enrolled repository")
	}
	expires := claims.ExpiresAt.Add(limits.GitHubRunnerCredentialTTL)
	registration := "github:" + strconv.FormatInt(claims.RepositoryID, 10) + ":" + strconv.FormatInt(claims.RepositoryOwnerID, 10)
	return capability.ProvisionSpec{Kind: capability.ProvisionActions, Team: string(team), RegistrationID: registration, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: registration + ":" + claims.RunID + ":" + claims.RunAttempt}, Grants: []capability.Grant{capability.NodesClaim}, ExpiresAt: &expires, Repositories: []string{"gh:" + strconv.FormatInt(d.RepositoryID, 10)}, Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{Receipt: receipt, RunID: d.RunID, JobID: d.JobID, RepositoryKey: "gh:" + strconv.FormatInt(d.RepositoryID, 10), Repository: d.Repository, Ref: workload.Request.Provenance.Ref(), SHA: workload.Request.Provenance.SHA(), WorkflowRunID: claims.RunID, WorkflowRunAttempt: claims.RunAttempt, WorkflowRef: claims.WorkflowRef, WorkflowSHA: claims.WorkflowSHA, RunnerImage: d.Runner, RunnerEnvironment: claims.RunnerEnvironment}}}, nil
}

func (s *Server) handleActionsApproval(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var input store.ActionsApproval
	if err := decodeEngineRequest(r, &input); err != nil {
		return err
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	if r.Method != http.MethodDelete {
		binding, err := tenant.GitHubRunnerBinding(r.Context(), input.RepositoryID)
		if err != nil {
			return engineLookupError(err)
		}
		_, repository, err := s.actionsToken(r.Context(), store.ActionsDispatch{InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, RepositoryOwnerID: binding.RepositoryOwnerID, Repository: binding.Repository}, "")
		if err != nil {
			return err
		}
		if problem := actionsFallbackRefProblem(input.Ref, repository); problem != "" {
			return &apierr.Error{Code: apierr.BadRequest, Message: problem}
		}
	}
	if err := tenant.PutActionsApproval(r.Context(), a, input, r.Method == http.MethodDelete); err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleRetryActionsDispatch(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeEngineRequest(r, &input); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "an operator retry needs a reason; delayed workflows can still spend GitHub startup minutes"}
	}
	if err := s.operator.RetryActionsDispatch(r.Context(), a, store.Team(r.PathValue("team")), r.PathValue("receipt"), input.Reason); err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "requested", "warning": "delayed workflows can still spend GitHub startup minutes"})
	return nil
}

func (s *Server) actionsApprovalURL(ctx context.Context, d store.ActionsDispatch) string {
	tenant, err := s.tenantForTeam(ctx, d.Team)
	if err == nil {
		inst, err := tenant.GitHubAppInstallation(ctx, d.InstallationID)
		if err == nil {
			return githubAppInstallationOut(inst).ManageURL
		}
	}
	return "https://github.com/settings/installations/" + strconv.FormatInt(d.InstallationID, 10)
}
