package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithAuthorization configures the policy router's committed-fact verifier,
// resource bindings and transport identity/CSRF adapters before Handler is built.
// Missing dependencies fail closed.
func (s *Server) WithAuthorization(authorization RouteAuthorization) *Server {
	s.authorization = authorization
	return s
}

func (s *Server) consoleEndpoints() []Endpoint {
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/operator/teams", Handle: s.handleOperatorTeams},
		{Method: "GET", Pattern: "/api/v1/operator/teams/{team}", Handle: s.handleOperatorTeam},
		{Method: "DELETE", Pattern: "/api/v1/operator/teams/{team}", Handle: s.handleOperatorDeleteTeam},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/trust", Handle: s.handleBillingTrustSet},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/grants", Handle: s.handleOperatorGrant},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/freeze", Handle: s.handleOperatorFreeze},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/unfreeze", Handle: s.handleOperatorUnfreeze},
		{Method: "DELETE", Pattern: "/api/v1/operator/accounts/{account}", Handle: s.handleOperatorDeleteAccount},
		{Method: "GET", Pattern: "/api/v1/operator/waitlist", Handle: s.handleOperatorWaitlist},
		{Method: "POST", Pattern: "/api/v1/operator/waitlist/approve", Handle: s.handleApproveWaitlist},
		{Method: "DELETE", Pattern: "/api/v1/operator/github-app/installations/{installation}", Handle: s.handleOperatorGitHubAppUnbind},
		{Method: "POST", Pattern: "/api/v1/operator/maintenance/reconcile-orphans", Handle: s.handleReconcileOrphans},
		{Method: "PUT", Pattern: "/api/v1/operator/credits/settings", Handle: s.handleCreditsSettingsSet},
	}
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
		endpoints[i].Decode = func(r *http.Request) (capability.RequestEvidence, error) {
			return capability.RequestEvidence{Target: capability.Target{Team: r.PathValue("team"), AccountID: r.PathValue("account")}}, nil
		}
	}
	return endpoints
}

func (s *Server) registerConsoleRoutes(mux *http.ServeMux) {
	router := NewPolicyRouter(s.authorization)
	endpoints := s.consoleEndpoints()
	var registrationError error
	for _, endpoint := range endpoints {
		if err := router.Register(endpoint); err != nil {
			registrationError = err
			break
		}
	}
	var handler http.Handler = router
	if registrationError != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writePolicyError(w, registrationError) })
	}
	for _, endpoint := range endpoints {
		mux.Handle(endpoint.Method+" "+endpoint.Pattern, noStore(handler))
	}
}

func (s *Server) consoleTenant(ctx context.Context, team string) (*store.Tenant, error) {
	tenant, err := s.store.ForTeam(ctx, store.Team(team))
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
	}
	return tenant, err
}
