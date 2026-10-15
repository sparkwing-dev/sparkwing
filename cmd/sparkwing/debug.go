package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func runDebug(args []string) error {
	if handleParentHelp(cmdDebug, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDebug, os.Stderr)
		return errors.New("debug: subcommand required")
	}
	switch args[0] {
	case "resume":
		ctx, stop, _ := interruptContext()
		defer stop()
		return runDebugResume(ctx, args[1:], os.Stdout, resolveDebugResumer)
	case "attach":
		return runDebugRead(context.Background(), "attach", args[1:])
	case "env":
		return runDebugRead(context.Background(), "env", args[1:])
	default:
		PrintHelp(cmdDebug, os.Stderr)
		return fmt.Errorf("debug: unknown subcommand %q", args[0])
	}
}
