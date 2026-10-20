package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Server) teamReadEndpoints() []Endpoint {
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/team/settings", Decode: authorityTarget, Handle: s.handleTeamSettings},
		{Method: "GET", Pattern: "/api/v1/schedules", Decode: authorityTarget, Handle: s.handleListCrons},
		{Method: "GET", Pattern: "/api/v1/schedules/{schedule}", Decode: authorityTarget, Handle: s.handleGetCron},
		{Method: "GET", Pattern: "/api/v1/team/invitations", Decode: authorityTarget, Handle: s.handleListInvitations},
		{Method: "GET", Pattern: "/api/v1/team/build-trust", Decode: authorityTarget, Handle: s.handleGetBuildTrust},
		{Method: "GET", Pattern: "/api/v1/team/github-app", Decode: authorityTarget, Handle: s.handleGitHubAppShow},
		{Method: "GET", Pattern: "/api/v1/team/github-app/installations/{installation}/repositories", Decode: authorityTarget, Handle: s.handleGitHubAppRepositories},
		{Method: "GET", Pattern: "/api/v1/team/github-app/triggers", Decode: authorityTarget, Handle: s.handleListGitHubAppTriggers},
		{Method: "GET", Pattern: "/api/v1/team/github-app/extra-repos", Decode: authorityTarget, Handle: s.handleListGitHubAppExtraRepos},
		{Method: "GET", Pattern: "/api/v1/team/billing", Decode: authorityTarget, Handle: s.meteredOnly(s.handleTeamBilling)},
		{Method: "GET", Pattern: "/api/v1/storage", Decode: authorityTarget, Handle: s.handleStorageShow},
	}
	if s.metricsAddr == "" {
		endpoints = append(endpoints, metricsEndpoint())
	}
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
	}
	return endpoints
}

func metricsEndpoint() Endpoint {
	return Endpoint{
		Method: "GET", Pattern: "/metrics", Authentication: capability.AuthCapability,
		Decode: authorityTarget,
		Handle: func(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
			metricsHandler().ServeHTTP(w, r)
			return nil
		},
	}
}
