package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
)

func runSDKInstall(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runSDKInstallCommand(ctx, args, installedVersion(), os.Stdin, os.Stdout, os.Stderr, sdkinstall.Run)
}

func runSDKInstallCommand(ctx context.Context, args []string, version string, input io.Reader, out, diagnostics io.Writer, install func(context.Context, string, io.Reader, io.Writer, io.Writer) error) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__sdk-install"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(false))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return install(ctx, version, input, out, diagnostics)
}
