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

func runSDKInstall(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runSDKInstallCommand(ctx, inv, os.Stdin, os.Stdout, os.Stderr, sdkinstall.Run)
}

func runSDKInstallCommand(ctx context.Context, inv clisurface.Invocation, input io.Reader, out, diagnostics io.Writer, install func(context.Context, io.Reader, io.Writer, io.Writer) error) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(false))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return install(ctx, input, out, diagnostics)
}
