// Handler for `sparkwing runs bounce`: restart one running node's
// process without failing the run it belongs to.
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

// nodeBouncer is the slice of the controller client this verb needs.
// Naming it keeps the two write paths -- a controller and this
// machine's own runs store -- interchangeable at the call site.
type nodeBouncer interface {
	RequestNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error)
}

// runRunsBounce records the intent to kill one node's process and run
// the node again. It returns as soon as the request is written: the
// kill and the re-run belong to whatever is supervising that process,
// which picks the request up on its next poll.
//
// Where the request is written depends on what can reach the run. A
// profile means a controller owns the run and the request goes over
// HTTP. A local run keeps its state in this home's store, which is the
// same row its loopback controller serves to its own runner -- and
// which works on a laptop with no dashboard and no profile, the
// machine `sparkwing run` serves.
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

// resolveNodeBouncer picks the writer that can reach the run, and the
// cleanup for whatever it opened: the profile's controller when one is
// named, a resident dashboard's controller when nothing narrower was
// asked for, and otherwise this home's runs store opened directly.
//
// An explicit --home skips the dashboard: it names the store the run
// is using, and a dashboard serving some other home would answer about
// runs the operator did not mean.
//
// The store path is not a lesser one. A local run's own controller is
// mounted on an ephemeral loopback port that belongs to that run and
// is announced to nobody, so writing the row into the home's store --
// which is exactly what that controller serves to the run's runner --
// is how an operator reaches a run in progress.
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

// storeBouncer adapts the store's four-argument request to the
// client's shape, stamping the OS user as the requester the way the
// controller stamps its authenticated principal.
type storeBouncer struct{ st *store.Store }

func (b storeBouncer) RequestNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error) {
	return b.st.RequestNodeBounce(ctx, runID, nodeID, whoami())
}
