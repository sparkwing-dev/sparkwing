// Command sparkwing-web is the dashboard pod's entry point: an HTTP
// server that serves the embedded Next.js bundle and proxies /api/*
// to the controller and logs-service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/internal/backend"
	"github.com/sparkwing-dev/sparkwing/v2/internal/web"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/otelutil"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/sparkwinglogs"
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
	controllerURL := fs.String("controller", "", "controller URL to read from (default: local SQLite)")
	logsURL := fs.String("logs", "", "logs service URL (default: read log files from local disk)")
	token := fs.String("token", "", "controller bearer token (also SPARKWING_AGENT_TOKEN)")
	apiURL := fs.String("api-url", "", "public API URL injected into the dashboard (default: same origin)")
	requireLogin := fs.Bool("require-login", false,
		"redirect unauthed browsers to /login (prod). Leave off for laptop-local dev where the tokens table is empty and login would loop.")
	_ = fs.Parse(args)

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-web"})
	defer tel.Shutdown(context.Background())

	if *token == "" {
		*token = os.Getenv("SPARKWING_AGENT_TOKEN")
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
		// controller /api/v1/runs, gated by runs.read under FOLLOWUPS #2.
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

	// Local mode: simple handler over local Paths.
	return web.Serve(ctx, paths, *addr)
}
