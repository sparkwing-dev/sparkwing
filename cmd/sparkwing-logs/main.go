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

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
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
	root := fs.String("root", "", "storage root; explicit roots keep shared/PVC creation modes subject to umask (default: private $SPARKWING_HOME/logs-service)")
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"),
		"controller URL used to resolve sw*_ tokens via /api/v1/auth/whoami; empty disables auth (env: SPARKWING_CONTROLLER_URL)")
	_ = fs.Parse(args)

	privateRoot := *root == ""
	if privateRoot {
		p, err := paths.DefaultPaths()
		if err != nil {
			return err
		}
		*root = filepath.Join(p.Root, "logs-service")
		if err := fssecure.EnsureDir(*root); err != nil {
			return fmt.Errorf("secure default storage root: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-logs"})
	defer func() { _ = tel.Shutdown(context.Background()) }()
	if privateRoot {
		return logs.ServePrivateWithTokens(ctx, *root, *addr, *controllerURL, nil)
	}
	return logs.ServeWithTokens(ctx, *root, *addr, *controllerURL, nil)
}
