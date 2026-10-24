package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// GitHubAppWriter rechecks authority and installation ownership when committing App configuration.
type GitHubAppWriter interface {
	Team() store.Team
	ListFailedGitHubDeliveries(context.Context, capability.Authorized) ([]store.FailedGitHubDelivery, error)
	ProcessGitHubDeliveryAgain(context.Context, capability.Authorized, string) error
	UnbindAuthorizedGitHubInstallation(context.Context, capability.Authorized, int64) error
	PutAuthorizedGitHubTrigger(context.Context, capability.Authorized, store.GitHubAppTrigger) (store.GitHubAppTrigger, error)
	DeleteAuthorizedGitHubTrigger(context.Context, capability.Authorized, int64, string) error
	PutAuthorizedGitHubAutomation(context.Context, capability.Authorized, store.GitHubAppAutomation) error
	DeleteAuthorizedGitHubAutomation(context.Context, capability.Authorized, int64) error
	SetAuthorizedGitHubExtraRepos(context.Context, capability.Authorized, string, []string, map[string]int64) ([]string, error)
}

var _ GitHubAppWriter = (*store.Tenant)(nil)

func (s *Server) githubAppWriter(r *http.Request, a capability.Authorized) (GitHubAppWriter, error) {
	return tenantPort[GitHubAppWriter](r.Context(), s, store.Team(a.Target.Team))
}

func (s *Server) githubAppEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/team/github-app/deliveries/failed", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleFailedGitHubDeliveries},
		{Method: "POST", Pattern: "/api/v1/team/github-app/deliveries/{delivery}/process-again", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleProcessGitHubDeliveryAgain},
		{Method: "DELETE", Pattern: "/api/v1/team/github-app/installations/{installation}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleGitHubAppUnbind},
		{Method: "PUT", Pattern: "/api/v1/team/github-app/triggers", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutGitHubAppTrigger},
		{Method: "DELETE", Pattern: "/api/v1/team/github-app/triggers", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteGitHubAppTrigger},
		{Method: "GET", Pattern: "/api/v1/team/github-app/automation", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleGitHubAppAutomation},
		{Method: "PUT", Pattern: "/api/v1/team/github-app/automation", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleGitHubAppAutomation},
		{Method: "DELETE", Pattern: "/api/v1/team/github-app/automation", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleGitHubAppAutomation},
		{Method: "PUT", Pattern: "/api/v1/team/github-app/extra-repos", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutGitHubAppExtraRepos},
	}
}

func githubAppRequestError(status int, err error) error {
	code := apierr.BadRequest
	switch status {
	case http.StatusNotFound:
		code = apierr.NotFound
	case http.StatusBadGateway:
		return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "GitHub repository discovery is unavailable"}, err)
	}
	return &apierr.Error{Code: code, Message: err.Error()}
}
