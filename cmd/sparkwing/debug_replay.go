package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// runDebugReplay mints a new replay run row + single nodes row and
// hands off to the pipeline binary's `replay-node` entrypoint for the
// actual single-node execution.
func runDebugReplay(args []string) error {
	t, err := parseReplayFlags(args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx := context.Background()

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}

	if t.on != "" {
		prof, err := resolveProfile(t.on)
		if err != nil {
			_ = st.Close()
			return err
		}
		if err := requireController(prof, "debug replay"); err != nil {
			_ = st.Close()
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		fmt.Fprintf(os.Stderr, "fetching dispatch state from %s for replay...\n", prof.Name)
		if err := orchestrator.SideloadRemoteForReplay(ctx, st, c, t.run, t.node); err != nil {
			_ = st.Close()
			return fmt.Errorf("sideload from %s: %w", prof.Name, err)
		}
	}

	// Don't defer close: we exec the pipeline binary below, which opens
	// the store itself. Closing here releases the file lock cleanly
	// before exec replaces the process.
	newRunID, err := orchestrator.MintReplayRun(ctx, st, t.run, t.node)
	_ = st.Close()
	if err != nil {
		return fmt.Errorf("mint replay run: %w", err)
	}

	sparkwingDir, err := findSparkwingDir()
	if err != nil {
		return fmt.Errorf("locate .sparkwing/: %w (replay needs the user's pipeline binary to reconstruct the input struct)", err)
	}

	fmt.Fprintf(os.Stderr, "minted replay run %s (replay of %s/%s)\n", newRunID, t.run, t.node)
	fmt.Fprintf(os.Stderr, "exec'ing pipeline binary: replay-node %s %s\n", newRunID, t.node)

	return compileAndExec(sparkwingDir,
		[]string{"replay-node", newRunID, t.node}, os.Environ(), compileOptions{})
}

type replayFlags struct {
	run  string
	node string
	on   string
}

func parseReplayFlags(args []string) (replayFlags, error) {
	fs := flag.NewFlagSet(cmdDebugReplay.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	nodeID := fs.String("node", "", "node id")
	on := fs.String("on", "", "profile name; sideloads the run+dispatch from the named controller, then replays locally")
	if err := parseAndCheck(cmdDebugReplay, fs, args); err != nil {
		return replayFlags{}, err
	}
	if *runID == "" || *nodeID == "" {
		return replayFlags{}, fmt.Errorf("%s: --run and --node are required", cmdDebugReplay.Path)
	}
	return replayFlags{run: *runID, node: *nodeID, on: *on}, nil
}
