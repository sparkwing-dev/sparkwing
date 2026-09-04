package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/sourceidentity"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type localFleetAuthority struct {
	url, localToken, localPrefix string
	server                       *controller.Server
	http                         *http.Server
	listener                     net.Listener
	source                       *localFleetSource
	store                        *store.Store
	logger                       *slog.Logger
}

const fleetTriggerSource = "pipeline-working-tree@foreground-fleet"

func startLocalFleetAuthority(st *store.Store, runID string, cfg fleet.Config, opts *Options, logger *slog.Logger) (*localFleetAuthority, error) {
	if logger == nil {
		logger = loopbackLogger()
	}
	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("fleet coordinator listen %s: %w", cfg.Listen, err)
	}
	fail := func(a *localFleetAuthority, err error) (*localFleetAuthority, error) {
		_ = lis.Close()
		if a != nil {
			a.closeResources()
		}
		return nil, err
	}
	a := &localFleetAuthority{listener: lis, store: st, logger: logger, url: cfg.PublicURL}

	if err := st.ResetExecutorLiveness(context.Background()); err != nil {
		return fail(a, fmt.Errorf("reset fleet executor liveness: %w", err))
	}
	allowedPrefixes := make(map[string]struct{}, len(cfg.Executors)+1)
	storedExecutors, err := st.ListExecutors(context.Background())
	if err != nil {
		return fail(a, fmt.Errorf("read fleet executor credential bindings: %w", err))
	}
	for _, enrolled := range cfg.Executors {
		var tokenPrefix string
		for _, stored := range storedExecutors {
			if stored.Name == enrolled.Name {
				tokenPrefix = stored.TokenPrefix
				break
			}
		}
		if tokenPrefix == "" {
			return fail(a, fmt.Errorf("fleet executor %q has no local credential enrollment; run `sparkwing fleet agents enroll`", enrolled.Name))
		}
		tok, err := st.LookupTokenByPrefix(tokenPrefix)
		if err != nil || !tok.IsValid(time.Now().UTC()) || tok.Kind != store.TokenKindRunner ||
			!tok.HasScope(controller.ScopeNodesClaim) || !tok.HasScope(controller.ScopeRunsState) {
			return fail(a, fmt.Errorf("fleet executor %q does not name a live runner credential with nodes.claim and runs.state", enrolled.Name))
		}
		if err := st.EnrollExecutor(context.Background(), tok.Prefix, enrolled.Registration(tok.Principal)); err != nil {
			return fail(a, fmt.Errorf("reconcile fleet executor %q: %w", enrolled.Name, err))
		}
		allowedPrefixes[tok.Prefix] = struct{}{}
	}

	localScopes := []string{controller.ScopeNodesClaim, controller.ScopeRunsState}
	localRaw, localToken, err := st.CreateToken("local-fleet:"+runID, store.TokenKindRunner, localScopes, 0, time.Now().UTC())
	if err != nil {
		return fail(a, fmt.Errorf("mint local fleet executor credential: %w", err))
	}
	a.localToken, a.localPrefix = localRaw, localToken.Prefix
	allowedPrefixes[localToken.Prefix] = struct{}{}
	if err := st.EnrollExecutor(context.Background(), localToken.Prefix, store.Executor{
		Name: cfg.Local.Name, Kind: "direct", Location: "coordinator",
		Capabilities: cfg.Local.Capabilities, BasePriority: 0, PriorityCeiling: 0,
		MaxConcurrent: cfg.Local.MaxConcurrent, Principal: localToken.Principal,
	}); err != nil {
		return fail(a, fmt.Errorf("enroll local fleet executor: %w", err))
	}

	sourceToken, err := newLoopbackToken()
	if err != nil {
		return fail(a, err)
	}
	a.source, err = startLocalFleetSource(opts.FleetSourceRoot, opts.FleetSourceBundle, opts.FleetSourceSHA, opts.FleetSourceRepoURL, sourceToken)
	if err != nil {
		return fail(a, err)
	}
	manifestDigest, err := sourceidentity.GitTreeManifestDigest(context.Background(), a.source.bareRepo, opts.FleetSourceSHA)
	if err != nil || manifestDigest != opts.FleetSourceManifestDigest {
		if err == nil {
			err = errors.New("served Fleet source does not match its manifest digest")
		}
		return fail(a, err)
	}
	git := opts.Git
	if git == nil {
		git = &sparkwing.Git{}
	}
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: runID, Pipeline: opts.Pipeline, Args: opts.Args,
		TriggerSource: fleetTriggerSource,
		GitBranch:     git.Branch, GitSHA: opts.FleetSourceSHA, Repo: git.Repo,
		RepoURL: opts.FleetSourceRepoURL, Status: "done", CreatedAt: time.Now().UTC(),
	}); err != nil {
		return fail(a, fmt.Errorf("record fleet source: %w", err))
	}

	ctrl := controller.New(st, logger).
		WithCacheCredentials(a.source.url, sourceToken).
		WithAuthenticator(controller.NewAuthenticator(st, loopbackAuthCacheTTL)).
		WithAssistedRunScope(runID)
	a.server = ctrl
	a.http = &http.Server{Handler: allowFleetTokenPrefixes(allowedPrefixes, ctrl.AssistedRunHandler()), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := a.http.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("fleet coordinator stopped", "run_id", runID, "err", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "fleet coordinator: %s\n", cfg.PublicURL)
	return a, nil
}

func allowFleetTokenPrefixes(allowed map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const bearer = "Bearer "
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, bearer) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw := strings.TrimSpace(strings.TrimPrefix(authorization, bearer))
		if len(raw) < store.PrefixLen {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, ok := allowed[raw[:store.PrefixLen]]; !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *localFleetAuthority) StopClaims() {
	if a != nil && a.server != nil {
		a.server.StopAssistedClaims()
	}
}

func (a *localFleetAuthority) Close() {
	if a == nil {
		return
	}
	a.StopClaims()
	if a.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.http.Shutdown(ctx)
		cancel()
	}
	a.closeResources()
}

func (a *localFleetAuthority) closeResources() {
	if a.source != nil {
		a.source.Close()
		a.source = nil
	}
	if a.store != nil && a.localPrefix != "" {
		if err := a.store.RevokeToken(a.localPrefix, time.Now().UTC()); err != nil {
			a.logger.Warn("fleet coordinator: revoke local executor credential", "err", err)
		}
		a.localPrefix = ""
	}
}
