package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func runDebug(inv clisurface.Invocation) error {
	switch inv.Command.Path {
	case "sparkwing debug resume":
		ctx, stop, _ := interruptContext()
		defer stop()
		return runDebugResume(ctx, inv, os.Stdout, resolveDebugResumer)
	case "sparkwing debug attach":
		return runDebugRead(context.Background(), inv)
	case "sparkwing debug env":
		return runDebugRead(context.Background(), inv)
	default:
		PrintHelp(cmdDebug, os.Stderr)
		return fmt.Errorf("debug: unknown subcommand %q", inv.Command.Path)
	}
}
