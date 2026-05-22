// Command sparkwing-web is the dashboard pod's entry point: an HTTP
// server that serves the embedded Next.js bundle and proxies /api/*
// to the controller and logs-service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	swpaths "github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-web:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-web", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:4343", "bind address")

	// Legacy cluster-mode flags. Kept for compatibility with existing
	// deployments; equivalent to --state-spec=controller://<profile>
	// plus --logs-spec=<sparkwing-logs URL> at the cmd line.
	controllerURL := fs.String("controller", "", "controller URL to read from (legacy; prefer --state-spec=controller://<profile>)")
	logsURL := fs.String("logs", "", "sparkwing-logs URL (legacy; prefer --logs-spec)")

	token := fs.String("token", "", "controller bearer token (also SPARKWING_AGENT_TOKEN)")
	apiURL := fs.String("api-url", "", "public API URL injected into the dashboard (default: same origin)")
	requireLogin := fs.Bool("require-login", false,
		"redirect unauthed browsers to /login (prod). Leave off for laptop-local dev where the tokens table is empty and login would loop.")

	// Shared-backends configuration. Specs accept the compact URL
	// form parsed by backend.ParseInlineSpec; config-file path points
	// at a backends.yaml (same shape sparkwing run consumes).
	configPath := fs.String("config", "", "path to backends.yaml; if unset, the repo's .sparkwing/backends.yaml is used when present")
	stateSpec := fs.String("state-spec", "", "inline state backend spec, e.g. postgres://user:pw@host/db or s3://bucket/prefix")
	logsSpecFlag := fs.String("logs-spec", "", "inline logs backend spec, e.g. s3://bucket/logs or stdout:")
	artifactsSpec := fs.String("artifacts-spec", "", "inline artifact backend spec; only consulted when state is object-store-backed")

	_ = fs.Parse(args)

	paths, err := swpaths.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-web"})
	defer func() { _ = tel.Shutdown(context.Background()) }()

	if *token == "" {
		*token = os.Getenv("SPARKWING_AGENT_TOKEN")
	}

	// Resolution precedence: explicit per-surface inline specs and/or
	// --config override the historical --controller/--logs/local SQLite
	// paths. The legacy flags remain so existing dashboard deployments
	// (k8s manifests pointing at the in-cluster controller) keep
	// working unchanged.
	usingNewConfig := *configPath != "" || *stateSpec != "" || *logsSpecFlag != "" || *artifactsSpec != ""

	if usingNewConfig {
		b, closer, err := openFromConfig(ctx, paths, *configPath, *stateSpec, *logsSpecFlag, *artifactsSpec)
		if err != nil {
			return err
		}
		defer func() { _ = closer.Close() }()
		opts := web.HandlerOptions{
			Backend:      b,
			Paths:        paths,
			Token:        *token,
			APIURL:       *apiURL,
			RequireLogin: *requireLogin,
		}
		return web.ServeWithOptions(ctx, opts, *addr)
	}

	// Cluster-mode wiring: --controller swaps state reads to HTTP,
	// --logs swaps log reads to the sparkwing-logs service. Each is
	// independent; set both for a full cluster dashboard.
	if *controllerURL != "" || *logsURL != "" {
		if *controllerURL == "" {
			return fmt.Errorf("--logs requires --controller (dashboard needs node list from controller)")
		}
		var logStore storage.LogStore
		if *logsURL != "" {
			// Pass the web pod's service token so the logs service
			// actually returns content; an unauthenticated request
			// comes back 401 and the dashboard renders "No logs
			// captured" even though the log is on disk.
			logStore = sparkwinglogs.New(*logsURL, nil, *token)
		}
		// Controller client needs the token too -- /api/runs on the
		// web's local mux delegates to ClientBackend which hits
		// controller /api/v1/runs.
		var c *client.Client
		if *token != "" {
			c = client.NewWithToken(*controllerURL, nil, *token)
		} else {
			c = client.New(*controllerURL, nil)
		}
		opts := web.HandlerOptions{
			Backend:       backend.NewClientBackend(c, logStore),
			Paths:         paths,
			ControllerURL: *controllerURL,
			LogsURL:       *logsURL,
			Token:         *token,
			APIURL:        *apiURL,
			RequireLogin:  *requireLogin,
		}
		return web.ServeWithOptions(ctx, opts, *addr)
	}

	// Local mode: simple handler over local Paths (today's default).
	return web.Serve(ctx, paths, *addr)
}

// openFromConfig resolves the backends.yaml + inline-spec precedence
// and opens the Backend the dashboard should serve from. Inline specs
// win over the config file's environment-resolved values.
func openFromConfig(
	ctx context.Context,
	paths swpaths.Paths,
	configPath, stateInline, logsInline, artifactsInline string,
) (backend.Backend, io.Closer, error) {
	var stateSpec, logsSpec, artifactsSpec *backends.Spec

	// 1. backends.yaml resolution. Mirror sparkwing run's path so a
	// single file describes the deployment for both binaries. The web
	// binary doesn't sit inside a .sparkwing/ project directory, so
	// pass empty for the repo-config path and rely on --config plus
	// the user-level ~/.config/sparkwing/backends.yaml.
	file, err := backends.ResolveWithOverlay("", configPath)
	if err != nil {
		return nil, nopCloser{}, fmt.Errorf("backends.yaml: %w", err)
	}
	envName, _, _ := backends.DetectEnvironment(file)
	eff := backends.Effective(file, envName, backends.Surfaces{})
	stateSpec = eff.State
	logsSpec = eff.Logs
	artifactsSpec = eff.Cache

	// 2. Inline overrides.
	if stateInline != "" {
		spec, err := backend.ParseInlineSpec(stateInline)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("--state-spec: %w", err)
		}
		stateSpec = spec
	}
	if logsInline != "" {
		spec, err := backend.ParseInlineSpec(logsInline)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("--logs-spec: %w", err)
		}
		logsSpec = spec
	}
	if artifactsInline != "" {
		spec, err := backend.ParseInlineSpec(artifactsInline)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("--artifacts-spec: %w", err)
		}
		artifactsSpec = spec
	}

	if stateSpec == nil {
		return nil, nopCloser{}, fmt.Errorf("no state backend configured; pass --state-spec or point --config at a backends.yaml that defines one")
	}

	return backend.FromSpecs(ctx, stateSpec, logsSpec, artifactsSpec, paths, nil)
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
