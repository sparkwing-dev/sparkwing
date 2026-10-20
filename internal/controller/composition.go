package controller

import (
	"context"
	"crypto/rand"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// New constructs a Server for a local, one-team deployment bound to the
// given store. A trigger it accepts is recorded for a runner to claim.
// Callers own the store's lifecycle; New never closes it.
func New(st *store.Store, logger *slog.Logger) *Server {
	return NewDeployment(st, deployment.Config{}, logger)
}

// NewDeployment constructs a Server for the declared deployment cfg. The
// deployment is fixed for the server's life: routes, authentication and
// the store's credit policy are built from it once.
func NewDeployment(st *store.Store, cfg deployment.Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	srv := &Server{
		deployment:         cfg,
		store:              st,
		operator:           st.AsOperator(),
		engineController:   &EngineController{Store: st},
		operatorSettings:   storeOperatorSettings{Store: st, Operator: st.AsOperator()},
		logger:             logger,
		loginLimit:         newLoginLimiter(),
		deliverySpools:     make(chan struct{}, githubDeliverySpools),
		queueTimeout:       15 * time.Minute,
		sessionMaxLifetime: DefaultSessionMaxLifetime,
		liveLogs:           newLiveLogs(),
		cronHolder:         defaultCronHolder(),
		leaseHolder:        rand.Text(),
		requestBudget:      newPrincipalBudget(RequestBudget{}),
		idleClaimPoll:      DefaultMaxIdleClaimPoll,
		downloadFree:       DefaultTeamDailyDownloadFreeBytes,
		downloadFunded:     DefaultTeamDailyDownloadFundedBytes,
	}
	if st != nil {
		if !cfg.Metering() {
			st.DisableCreditMetering()
		}
		srv.resolveTenant = func(ctx context.Context, team store.Team) (TenantHandle, error) { return st.ForTeam(ctx, team) }
		srv.credentialIssuer = st
		st.SetConsolePolicy(srv.consoleAuthority)
		st.SetIntakePolicy(srv)
	}
	srv.authorization = srv.storeRouteAuthorization()
	srv.recordQueueActivity(time.Now())
	return srv
}
