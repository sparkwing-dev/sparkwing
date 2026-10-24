package controller

import (
	"context"
	"crypto/rand"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
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
		engineController:   NewEngineController(st, EngineController{}),
		logger:             slog.New(CredentialSafeLogHandler(logger.Handler())),
		loginLimit:         newLoginLimiter(),
		deliverySpools:     make(chan struct{}, githubDeliverySpools),
		sessionMaxLifetime: defaultSessionMaxLifetime,
		liveLogs:           newLiveLogs(),
		cronHolder:         defaultCronHolder(),
		leaseHolder:        rand.Text(),
		requestBudget:      newPrincipalBudget(RequestBudget{}),
		idleClaimPoll:      DefaultMaxIdleClaimPoll,
		downloadFree:       DefaultTeamDailyDownloadFreeBytes,
		downloadFunded:     DefaultTeamDailyDownloadFundedBytes,
	}
	if st != nil {
		srv.operator = st.AsOperator()
		srv.operatorCapabilities = srv.operator
		srv.dataController = NewDataController(st, DataController{})
		srv.operatorSettings = srv.operator
		srv.accountReads = srv.operator
		srv.accountWrites = srv.operator
		srv.identityLinkWrites = srv.operator
		srv.consoleWrites = st.AsConsole(srv.operatorAccount)
		if !cfg.Metering() {
			st.DisableCreditMetering()
		}
		srv.resolveTenant = storeTenantResolver(st)
		srv.credentialIssuer = srv.operator
	}
	srv.authorization = srv.storeRouteAuthorization()
	srv.recordQueueActivity(time.Now())
	return srv
}

func storeTenantResolver(st *store.Store) func(context.Context, store.Team) (TenantHandle, error) {
	if st == nil {
		return nil
	}
	return func(ctx context.Context, team store.Team) (TenantHandle, error) { return st.ForTeam(ctx, team) }
}

// NewEngineController binds global authority ports and the shared tenant resolver.
func NewEngineController(st *store.Store, cfg EngineController) *EngineController {
	if st == nil {
		return &cfg
	}
	op := st.AsOperator()
	cfg.authority, cfg.operator = op, op
	if cfg.Units == nil {
		cfg.Units = op
	}
	if cfg.Launcher == nil {
		cfg.Launcher = storeLauncherSync{op}
	}
	cfg.authorization = routepolicy.Authorization{Authenticator: capability.NewAuthenticator(op), Bindings: op}
	if cfg.ResolveTenant == nil {
		cfg.ResolveTenant = storeTenantResolver(st)
	}
	return &cfg
}

// NewDataController binds persisted data access to the shared tenant resolver.
func NewDataController(st *store.Store, cfg DataController) *DataController {
	if st != nil {
		cfg.bind(st.AsOperator(), storeTenantResolver(st))
	}
	return &cfg
}
