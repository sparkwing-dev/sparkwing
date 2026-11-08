package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) actionsToken(ctx context.Context, d store.ActionsDispatch) (githubapp.Token, error) {
	if s.githubApp == nil {
		return githubapp.Token{}, s.githubAppUnavailable()
	}
	inst, err := s.githubApp.client.Installation(ctx, d.InstallationID)
	if err != nil {
		return githubapp.Token{}, err
	}
	if inst.ID != d.InstallationID || inst.AppID != s.githubApp.client.AppID() || inst.Account.ID != d.RepositoryOwnerID || inst.SuspendedAt != nil {
		return githubapp.Token{}, actionsExchangeRefusal()
	}
	token, err := s.githubApp.client.InstallationTokenByIDs(ctx, d.InstallationID, []int64{d.RepositoryID}, map[string]string{"actions": "write", "contents": "read"})
	if err != nil {
		return githubapp.Token{}, err
	}
	repo, err := s.githubApp.client.RepositoryByID(ctx, token.Token, d.RepositoryID)
	if err != nil {
		return githubapp.Token{}, err
	}
	if repo.ID != d.RepositoryID || repo.Owner.ID != d.RepositoryOwnerID || repo.FullName != d.Repository {
		return githubapp.Token{}, actionsExchangeRefusal()
	}
	return token, nil
}

func (s *Server) actionsRepair(ctx context.Context, d store.ActionsDispatch, err error) string {
	if errors.Is(err, githubapp.ErrPermissionMissing) {
		return "approve Actions write for the GitHub App: " + s.actionsApprovalURL(ctx, d)
	}
	return "GitHub workflow unavailable; check the App installation, repository access and approved workflow"
}

func (s *Server) processActionsDispatch(ctx context.Context) (bool, error) {
	d, err := s.operator.NextActionsDispatch(ctx)
	if err != nil || d == nil {
		return false, err
	}
	token, err := s.actionsToken(ctx, *d)
	if err != nil {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", s.actionsRepair(ctx, *d, err))
	}
	repo, ok := store.ParseGitHubRepo(d.Repository)
	if !ok {
		return true, store.ErrInvalidInput
	}
	sha, err := s.githubApp.client.ResolveCommit(ctx, d.InstallationID, repo.Owner, repo.Name, d.WorkflowRef)
	if err != nil {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", "approved workflow ref cannot be resolved; check the branch or tag")
	}
	_, present, err := s.githubApp.client.FileAtCommit(ctx, d.InstallationID, d.RepositoryID, repo.Owner, repo.Name, sha, d.Workflow)
	if err != nil || !present {
		return true, s.operator.FinishActionsDispatch(ctx, d.Receipt, "refused", "commit the approved workflow with workflow_dispatch to the default branch and approved ref")
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

func (s *Server) processPendingActionsCancellations(ctx context.Context) error {
	pending, err := s.operator.PendingActionsCancellations(ctx)
	if err != nil {
		return err
	}
	for _, d := range pending {
		token, err := s.actionsToken(ctx, d)
		if err != nil {
			return err
		}
		if err := s.githubApp.client.CancelActionsRun(ctx, token.Token, d.Repository, d.ProviderRunID); err != nil {
			return err
		}
		if err := s.operator.FinishActionsCancellation(ctx, d.Receipt); err != nil {
			return err
		}
	}
	return nil
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
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	d, err := s.operator.ReadActionsDispatch(ctx, team, receipt)
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	if d.RepositoryID != claims.RepositoryID || d.RepositoryOwnerID != claims.RepositoryOwnerID || d.Repository != claims.Repository || claims.WorkflowRef != d.Repository+"/"+d.Workflow+"@"+d.WorkflowRef || claims.WorkflowSHA != d.WorkflowSHA || claims.SHA != d.WorkflowSHA || claims.Ref != d.WorkflowRef || !slices.Contains([]string{"sending", "accepted", "uncertain"}, d.State) {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	token, err := s.actionsToken(ctx, d)
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	run, err := s.githubApp.client.ActionsRun(ctx, token.Token, d.Repository, claims.RunID)
	if err != nil {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	if strconv.FormatInt(run.ID, 10) != claims.RunID || strconv.FormatInt(run.Attempt, 10) != claims.RunAttempt || run.Event != "workflow_dispatch" || run.Title != "sparkwing:"+receipt || run.Path != d.Workflow || run.SHA != d.WorkflowSHA || run.Repository.ID != d.RepositoryID || run.Repository.Owner.ID != d.RepositoryOwnerID {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
	}
	labels, err := s.githubApp.client.ActionsJobLabels(ctx, token.Token, d.Repository, claims.RunID, claims.RunAttempt, claims.CheckRunID)
	if err != nil || !slices.Contains(labels, d.Runner) || slices.Contains(labels, "self-hosted") {
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
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
		return capability.ProvisionSpec{}, actionsExchangeRefusal()
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
