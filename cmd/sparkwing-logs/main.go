// Command sparkwing-logs is the logs-service pod's entry point:
// an HTTP service fronting file-per-node log storage. Worker pods
// POST log lines, the web pod GETs them back.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/logs"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/otelutil"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-logs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-logs", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:4345", "bind address")
	root := fs.String("root", "", "storage root (default: $SPARKWING_HOME/logs-service)")
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"),
		"controller URL used to resolve sw*_ tokens via /api/v1/auth/whoami; empty disables auth (env: SPARKWING_CONTROLLER_URL)")
	_ = fs.Parse(args)

	if *root == "" {
		paths, err := orchestrator.DefaultPaths()
		if err != nil {
			return err
		}
		*root = filepath.Join(paths.Root, "logs-service")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-logs"})
	defer tel.Shutdown(context.Background())
	return logs.ServeWithTokens(ctx, *root, *addr, *controllerURL, nil)
}
