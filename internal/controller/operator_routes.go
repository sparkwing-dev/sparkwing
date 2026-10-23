package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// WithAuthorization configures the policy router's committed-fact verifier,
// resource bindings and transport identity/CSRF adapters before Handler is built.
// Missing dependencies fail closed.
func (s *Server) WithAuthorization(authorization routepolicy.Authorization) *Server {
	s.authorization = authorization
	return s
}

func (s *Server) operatorEndpoints() []controllerwire.Endpoint {
	endpoints := []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/operator/limits", Handle: s.handleComputeLimitsShow},
		{Method: "PUT", Pattern: "/api/v1/operator/limits", Handle: s.handleComputeLimitsSet},
		{Method: "GET", Pattern: "/api/v1/operator/signups", Handle: s.handleSignUpStatus},
		{Method: "PUT", Pattern: "/api/v1/operator/signups", Handle: s.handleSetSignUp},
		{Method: "GET", Pattern: "/api/v1/operator/object-store", Handle: s.handleOperatorObjectStore},
		{Method: "GET", Pattern: "/api/v1/operator/object-store/breaker", Handle: s.handleObjectStoreBreaker},
		{Method: "POST", Pattern: "/api/v1/operator/object-store/reset-breaker", Handle: s.handleResetObjectStoreBreaker},
		{Method: "PUT", Pattern: "/api/v1/operator/object-store/settings", Handle: s.handleSetStorageSettings},
		{Method: "PUT", Pattern: "/api/v1/operator/object-store/quotas/{team}", Handle: s.handleSetStorageQuota},
		{Method: "PUT", Pattern: "/api/v1/operator/object-store/quotas/{team}/allowance", Handle: s.handleSetStorageAllowance},

		{Method: "GET", Pattern: "/api/v1/operator/teams", Handle: s.handleOperatorTeams},
		{Method: "POST", Pattern: "/api/v1/operator/teams", Handle: s.handleOperatorCreateTeam},
		{Method: "GET", Pattern: "/api/v1/operator/teams/{team}", Handle: s.handleOperatorTeam},
		{Method: "DELETE", Pattern: "/api/v1/operator/teams/{team}", Handle: s.handleOperatorDeleteTeam},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/trust", Handle: s.handleBillingTrustSet},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/refunds", Handle: s.handleOperatorRefund},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/grants", Handle: s.handleOperatorGrant},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/freeze", Handle: s.handleOperatorFreeze},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/unfreeze", Handle: s.handleOperatorUnfreeze},
		{Method: "DELETE", Pattern: "/api/v1/operator/accounts/{account}", Handle: s.handleOperatorDeleteAccount},
		{Method: "GET", Pattern: "/api/v1/operator/waitlist", Handle: s.handleOperatorWaitlist},
		{Method: "GET", Pattern: "/api/v1/operator/standing-requests", Handle: s.handleOperatorStandingRequests},
		{Method: "POST", Pattern: "/api/v1/operator/teams/{team}/standing-request/decline", Handle: s.handleOperatorDeclineStanding},
		{Method: "POST", Pattern: "/api/v1/operator/waitlist/approve", Handle: s.handleApproveWaitlist},
		{Method: "DELETE", Pattern: "/api/v1/operator/github-app/installations/{installation}", Handle: s.handleOperatorGitHubAppUnbind},
		{Method: "PUT", Pattern: "/api/v1/operator/credits/settings", Handle: s.handleCreditsSettingsSet},
		{Method: "POST", Pattern: "/api/v1/operator/runners/{runner}/metered", Handle: s.meteredOnly(s.handleSetRunnerMetered)},
		{Method: "POST", Pattern: "/api/v1/operator/secrets/rotate", Handle: s.handleRotateSealedValues},
	}
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
		endpoints[i].Decode = func(r *http.Request) (capability.RequestEvidence, error) {
			return capability.RequestEvidence{Target: capability.Target{Team: r.PathValue("team"), AccountID: r.PathValue("account")}}, nil
		}
	}
	return endpoints
}

func (s *Server) registerPolicyEndpoints(mux *registeredMux, endpoints []controllerwire.Endpoint) {
	router := routepolicy.New(controllerAuthorization(s.authorization, s.resolveTenant))
	router.Mux = mux.ServeMux

	for _, endpoint := range endpoints {
		if err := router.Register(s.tenantEndpoint(endpoint)); err != nil {
			panic(err)
		}
	}
	mux.registrations = append(mux.registrations, router.Registrations()...)
	paths := make(map[string]bool)
	for _, endpoint := range endpoints {
		if !paths[endpoint.Pattern] {
			mux.HandleFunc(endpoint.Pattern, func(w http.ResponseWriter, r *http.Request) {
				routepolicy.WriteRouteMiss(w, router.AllowedMethods(r))
			})
			paths[endpoint.Pattern] = true
		}
	}
}

func (s *Server) consoleTenant(ctx context.Context, team string) (*store.Tenant, error) {
	tenant, err := s.tenantForTeam(ctx, store.Team(team))
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, &apierr.Error{Code: apierr.NotFound, Message: "team is not registered"}
	}
	return tenant, err
}
