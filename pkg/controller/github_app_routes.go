package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// GitHubAppWriter rechecks authority and installation ownership when committing App configuration.
type GitHubAppWriter interface {
	Team() store.Team
	UnbindAuthorizedGitHubInstallation(context.Context, capability.Authorized, int64) error
	PutAuthorizedGitHubTrigger(context.Context, capability.Authorized, store.GitHubAppTrigger) (store.GitHubAppTrigger, error)
	DeleteAuthorizedGitHubTrigger(context.Context, capability.Authorized, int64, string) error
	PutAuthorizedGitHubAutomation(context.Context, capability.Authorized, store.GitHubAppAutomation) error
	DeleteAuthorizedGitHubAutomation(context.Context, capability.Authorized, int64) error
	SetAuthorizedGitHubExtraRepos(context.Context, capability.Authorized, string, []string, map[string]int64) ([]string, error)
}

// WithGitHubAppWriter supplies tenant App transactions before Handler is constructed.
func (s *Server) WithGitHubAppWriter(resolve func(context.Context, store.Team) (GitHubAppWriter, error)) *Server {
	s.resolveGitHubAppWriter = resolve
	return s
}

func (s *Server) githubAppWriter(r *http.Request, a capability.Authorized) (GitHubAppWriter, error) {
	if a.Target.Team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer GitHubAppWriter
	var err error
	if s.resolveGitHubAppWriter != nil {
		writer, err = s.resolveGitHubAppWriter(r.Context(), store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.reportingTenant(r, a)
		writer, _ = any(tenant).(GitHubAppWriter)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(a.Target.Team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

func (s *Server) githubAppEndpoints() []Endpoint {
	return []Endpoint{
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
		return &apierr.Error{Code: apierr.Unavailable, Message: "GitHub repository discovery is unavailable"}
	}
	return &apierr.Error{Code: code, Message: err.Error()}
}
