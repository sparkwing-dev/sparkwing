package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nodeBouncer interface {
	RequestNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error)
}

func runRunsBounce(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsBounce.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run id owning the node")
	nodeID := fs.String("node", "", "node id to bounce")
	on := fs.String("profile", "", "profile name for remote runs; omit for local runs")
	home := fs.String("home", "", "sparkwing home holding the run (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdJobsBounce, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run and --node)", cmdJobsBounce.Path, rest[0])
	}
	id := normalizeRunID(*runID)
	if id == "" || *nodeID == "" {
		return fmt.Errorf("%s: --run RUN_ID and --node NODE_ID are required", cmdJobsBounce.Path)
	}

	bouncer, cleanup, err := resolveNodeBouncer(*on, *home)
	if err != nil {
		return err
	}
	defer cleanup()

	b, err := bouncer.RequestNodeBounce(ctx, id, *nodeID)
	if err != nil {
		// safety: the refusal is passed through rather than reworded. Both
		// writers already say which id was missing or which status the
		// node is in, and that sentence is the answer the operator came
		// for.
		return fmt.Errorf("%s: %w", cmdJobsBounce.Path, err)
	}
	fmt.Fprintf(os.Stdout, "bounce requested for %s/%s (request %d)\n", id, *nodeID, b.Seq)
	fmt.Fprintln(os.Stdout,
		"the node's process will be stopped and the node re-run from its first step; the run keeps going")
	return nil
}

func resolveNodeBouncer(on, home string) (nodeBouncer, func(), error) {
	noop := func() {}
	if on != "" {
		c, _, err := resolveRunsClient(on, cmdJobsBounce.Path)
		return c, noop, err
	}
	if home == "" && orchestrator.ResolveDevEnvURL("SPARKWING_CONTROLLER_URL") != "" {
		if c, _, err := resolveRunsClient("", cmdJobsBounce.Path); err == nil {
			return c, noop, nil
		}
	}
	paths, err := submitPaths(home)
	if err != nil {
		return nil, noop, err
	}
	if _, serr := os.Stat(paths.StateDB()); serr != nil {
		return nil, noop, fmt.Errorf("%s: no runs store at %s (is this the home the run is using?)",
			cmdJobsBounce.Path, paths.StateDB())
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, noop, err
	}
	return storeBouncer{st}, func() { _ = st.Close() }, nil
}

type storeBouncer struct{ st *store.Store }

func (b storeBouncer) RequestNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error) {
	return b.st.RequestNodeBounce(ctx, runID, nodeID, whoami())
}
