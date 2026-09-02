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
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
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
	cacheURL := fs.String("cache", "",
		"sparkwing-cache URL to include in the services health panel. Probe only -- "+
			"the dashboard reads nothing else from the cache. Empty leaves it off the panel.")

	token := fs.String("token", "", "controller bearer token (also SPARKWING_AGENT_TOKEN)")
	_ = fs.String("api-url", "", "deprecated; the dashboard proxies the API on its own origin")
	requireLogin := fs.Bool("require-login", false,
		"require controller-backed browser sessions; needs --controller or a profile with controller.url. Leave off for laptop-local dev.")
	allowUnauthenticatedRemote := fs.Bool("allow-unauthenticated-remote", false,
		"serve a token-backed dashboard without --require-login on a non-loopback address, handing the controller to every caller that reaches it")
	trustedProxyCIDRsRaw := fs.String("trusted-proxy-cidrs", "",
		"comma-separated proxy source CIDRs allowed to supply X-Forwarded-For; empty ignores forwarded headers")

	profileName := fs.String("profile", "", "storage profile name from ~/.config/sparkwing/profiles.yaml whose surfaces the dashboard reads")
	stateSpec := fs.String("state-spec", "", "inline state backend spec, e.g. postgres://user:pw@host/db or s3://bucket/prefix")
	logsSpecFlag := fs.String("logs-spec", "", "inline logs backend spec, e.g. s3://bucket/logs or stdout:")
	artifactsSpec := fs.String("artifacts-spec", "", "inline artifact backend spec; only consulted when state is object-store-backed")

	if err := fs.MarkDeprecated("api-url", "the dashboard proxies the API on its own origin"); err != nil {
		return err
	}

	_ = fs.Parse(args)
	trustedProxyCIDRs, err := ratelimit.ParseTrustedProxyCIDRs(*trustedProxyCIDRsRaw)
	if err != nil {
		return fmt.Errorf("--trusted-proxy-cidrs: %w", err)
	}

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
		b, closer, profileControllerURL, err := openFromConfig(ctx, paths, *profileName, *stateSpec, *logsSpecFlag, *artifactsSpec)
		if err != nil {
			return err
		}
		defer func() { _ = closer.Close() }()
		authControllerURL := resolveAuthControllerURL(*controllerURL, profileControllerURL)
		if err := validateLoginBackend(*requireLogin, authControllerURL); err != nil {
			return err
		}
		opts := web.HandlerOptions{
			Backend:           b,
			Paths:             paths,
			AuthControllerURL: authControllerURL,
			CacheURL:          *cacheURL,
			Token:             *token,
			RequireLogin:      *requireLogin,
			TrustedProxyCIDRs: trustedProxyCIDRs,

			AllowUnauthenticatedRemote: *allowUnauthenticatedRemote,
		}
		return web.ServeWithOptions(ctx, opts, *addr)
	}

	if *controllerURL != "" || *logsURL != "" {
		if *controllerURL == "" {
			return fmt.Errorf("--logs requires --controller (dashboard needs node list from controller)")
		}
		if err := validateLoginBackend(*requireLogin, *controllerURL); err != nil {
			return err
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
			Backend:           backend.NewClientBackend(c, logStore),
			Paths:             paths,
			ControllerURL:     *controllerURL,
			LogsURL:           *logsURL,
			CacheURL:          *cacheURL,
			Token:             *token,
			RequireLogin:      *requireLogin,
			TrustedProxyCIDRs: trustedProxyCIDRs,

			AllowUnauthenticatedRemote: *allowUnauthenticatedRemote,
		}
		return web.ServeWithOptions(ctx, opts, *addr)
	}
	if err := validateLoginBackend(*requireLogin, ""); err != nil {
		return err
	}

	return web.Serve(ctx, paths, *addr, trustedProxyCIDRs)
}

func validateLoginBackend(requireLogin bool, controllerURL string) error {
	if requireLogin && controllerURL == "" {
		return fmt.Errorf("--require-login requires a controller session backend; pass --controller URL or select --profile NAME with controller.url")
	}
	return nil
}

func resolveAuthControllerURL(explicit, profileURL string) string {
	if explicit != "" {
		return explicit
	}
	return profileURL
}

func openFromConfig(
	ctx context.Context,
	paths swpaths.Paths,
	profileName, stateInline, logsInline, artifactsInline string,
) (backend.Backend, io.Closer, string, error) {
	var stateSpec, logsSpec, artifactsSpec *backends.Spec
	var lookup storeurl.ProfileLookup
	var profileControllerURL string

	if profileName != "" {
		path, err := profile.DefaultPath()
		if err != nil {
			return nil, nopCloser{}, "", err
		}
		cfg, err := profile.Load(path)
		if err != nil {
			return nil, nopCloser{}, "", err
		}
		p, _, err := profile.Resolve(profileName, cfg)
		if err != nil {
			return nil, nopCloser{}, "", fmt.Errorf("--profile %s: %w", profileName, err)
		}
		profileControllerURL = p.ControllerURL()
		stateSpec, logsSpec, artifactsSpec = profileWebSpecs(p)
		if p.ControllerURL() != "" {
			lookup = func(string) (string, string, error) { return p.ControllerURL(), p.ControllerToken(), nil }
		}
	}

	if stateInline != "" {
		spec, err := backend.ParseInlineSpec(stateInline)
		if err != nil {
			return nil, nopCloser{}, "", fmt.Errorf("--state-spec: %w", err)
		}
		stateSpec = spec
	}
	if logsInline != "" {
		spec, err := backend.ParseInlineSpec(logsInline)
		if err != nil {
			return nil, nopCloser{}, "", fmt.Errorf("--logs-spec: %w", err)
		}
		logsSpec = spec
	}
	if artifactsInline != "" {
		spec, err := backend.ParseInlineSpec(artifactsInline)
		if err != nil {
			return nil, nopCloser{}, "", fmt.Errorf("--artifacts-spec: %w", err)
		}
		artifactsSpec = spec
	}

	if stateSpec == nil {
		return nil, nopCloser{}, "", fmt.Errorf("no state backend configured; pass --state-spec or --profile <name> with a profile that declares a state surface (or a controller)")
	}

	b, closer, err := backend.FromSpecs(ctx, stateSpec, logsSpec, artifactsSpec, paths, lookup)
	return b, closer, profileControllerURL, err
}

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
