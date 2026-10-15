package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
		return runDebugRelease(args[1:])
	case "attach":
		return runDebugRead(context.Background(), "attach", args[1:])
	case "env":
		return runDebugRead(context.Background(), "env", args[1:])
	default:
		PrintHelp(cmdDebug, os.Stderr)
		return fmt.Errorf("debug: unknown subcommand %q", args[0])
	}
}

type debugTargetFlags struct {
	run  string
	node string
	on   string
}

func parseDebugTarget(cmd Command, args []string) (debugTargetFlags, error) {
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	nodeID := fs.String("job", "", "job id")
	on := fs.String("profile", "", "controller profile name")
	if err := parseAndCheck(cmd, fs, args); err != nil {
		return debugTargetFlags{}, err
	}
	if *runID == "" || *nodeID == "" {
		return debugTargetFlags{}, fmt.Errorf("%s: --run and --job are required", cmd.Path)
	}
	return debugTargetFlags{run: *runID, node: *nodeID, on: *on}, nil
}

func runDebugRelease(args []string) error {
	t, err := parseDebugTarget(cmdDebugRelease, args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	releasedBy := whoami()
	ctx := context.Background()

	if t.on != "" {
		prof, err := resolveProfile(t.on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "debug resume"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		if err := c.ReleaseDebugPause(ctx, t.run, t.node, releasedBy, store.PauseReleaseManual); err != nil {
			return fmt.Errorf("release %s/%s: %w", t.run, t.node, err)
		}
		fmt.Fprintf(os.Stdout, "released %s/%s on %s\n", t.run, t.node, prof.Name)
		return nil
	}

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.ReleaseDebugPause(ctx, t.run, t.node, releasedBy, store.PauseReleaseManual); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no active pause for %s/%s (already released or never paused)", t.run, t.node)
		}
		return fmt.Errorf("release %s/%s: %w", t.run, t.node, err)
	}
	fmt.Fprintf(os.Stdout, "released %s/%s\n", t.run, t.node)
	return nil
}

func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "cli"
}
