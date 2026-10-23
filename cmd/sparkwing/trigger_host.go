package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func runTriggerHost(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runLocalHostCommand(ctx, inv, os.Stdout, localRunDependencies{resolve: resolveLocalRunRuntime, version: installedVersion()})
}

func runLocalHostCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, deps localRunDependencies) (resultErr error) {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(false))
	}
	options := localRunOptions{standaloneReason: strings.TrimSpace(inv.Value("standalone-reason")), stateDB: inv.Value("state-db"), environmentFile: inv.Value("env-file")}
	if len(inv.Args) != 0 || inv.Value("run") == "" || options.standaloneReason == "" || !filepath.IsAbs(options.stateDB) || (options.environmentFile != "" && !filepath.IsAbs(options.environmentFile)) {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--run RUN, --standalone-reason REASON and absolute --state-db PATH are required; --env-file must be absolute when present"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := deps.resolve(ctx, options)
	if err != nil {
		return err
	}
	if runtime.close != nil {
		defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()
	}
	if runtime.controller == nil || runtime.bind == nil || runtime.close == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "standalone run integration has incomplete controller or host bindings"}
	}
	completion, err := hostLocalRun(ctx, runtime, inv.Value("run"), "", deps.version)
	if err != nil {
		return err
	}
	return statusExitCode(completion.Outcome)
}
