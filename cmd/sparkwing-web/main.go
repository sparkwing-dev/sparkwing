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
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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

	controllerURL := fs.String("controller", "", "controller URL to read from (legacy; prefer --state-spec=controller://<profile>)")
	logsURL := fs.String("logs", "", "sparkwing-logs URL (legacy; prefer --logs-spec)")

	token := fs.String("token", "", "controller bearer token (also SPARKWING_AGENT_TOKEN)")
	apiURL := fs.String("api-url", "", "public API URL injected into the dashboard (default: same origin)")
	requireLogin := fs.Bool("require-login", false,
		"redirect unauthed browsers to /login (prod). Leave off for laptop-local dev where the tokens table is empty and login would loop.")

	profileName := fs.String("profile", "", "storage profile name from ~/.config/sparkwing/profiles.yaml whose surfaces the dashboard reads")
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

	usingNewConfig := *profileName != "" || *stateSpec != "" || *logsSpecFlag != "" || *artifactsSpec != ""

	if usingNewConfig {
		b, closer, err := openFromConfig(ctx, paths, *profileName, *stateSpec, *logsSpecFlag, *artifactsSpec)
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

	if *controllerURL != "" || *logsURL != "" {
		if *controllerURL == "" {
			return fmt.Errorf("--logs requires --controller (dashboard needs node list from controller)")
		}
		var logStore storage.LogStore
		if *logsURL != "" {
			logStore = sparkwinglogs.New(*logsURL, nil, *token)
		}
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

	return web.Serve(ctx, paths, *addr)
}

// openFromConfig resolves the profile + inline-spec precedence and opens
// the Backend the dashboard should serve from. Inline specs win over the
// profile's surfaces.
func openFromConfig(
	ctx context.Context,
	paths swpaths.Paths,
	profileName, stateInline, logsInline, artifactsInline string,
) (backend.Backend, io.Closer, error) {
	var stateSpec, logsSpec, artifactsSpec *backends.Spec
	var lookup storeurl.ProfileLookup

	if profileName != "" {
		path, err := profile.DefaultPath()
		if err != nil {
			return nil, nopCloser{}, err
		}
		cfg, err := profile.Load(path)
		if err != nil {
			return nil, nopCloser{}, err
		}
		p, _, err := profile.Resolve(profileName, cfg)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("--profile %s: %w", profileName, err)
		}
		stateSpec, logsSpec, artifactsSpec = profileWebSpecs(p)
		if p.ControllerURL() != "" {
			lookup = func(string) (string, string, error) { return p.ControllerURL(), p.ControllerToken(), nil }
		}
	}

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
		return nil, nopCloser{}, fmt.Errorf("no state backend configured; pass --state-spec or --profile <name> with a profile that declares a state surface (or a controller)")
	}

	return backend.FromSpecs(ctx, stateSpec, logsSpec, artifactsSpec, paths, lookup)
}

// profileWebSpecs derives the dashboard's state/logs/cache specs from a
// resolved profile: explicit surfaces as declared, or -- for a
// controller-only profile -- every surface routed through the
// controller. A bare/laptop profile yields nil specs (the caller then
// requires an inline --state-spec).
func profileWebSpecs(p *profile.Profile) (state, logs, cache *backends.Spec) {
	surf := p.Surfaces()
	if surf.State == nil && surf.Logs == nil && surf.Cache == nil && p.ControllerURL() != "" {
		c := func() *backends.Spec { return &backends.Spec{Type: backends.TypeController, Controller: p.Name} }
		return c(), c(), c()
	}
	return surf.State, surf.Logs, surf.Cache
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
