package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func runNodeCommand(args []string) error {
	fs := flag.NewFlagSet("run-node", flag.ExitOnError)
	controllerURL := fs.String("controller", orchestrator.ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"), "controller base URL")
	logsURL := fs.String("logs", orchestrator.ResolveDevEnvURL("SPARKWING_LOGS_URL"), "logs-service URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	runID := fs.Arg(0)
	if runID == "" {
		runID = os.Getenv("SPARKWING_RUN_ID")
	}
	nodeID := fs.Arg(1)
	if nodeID == "" {
		nodeID = os.Getenv("SPARKWING_NODE_ID")
	}
	if *controllerURL == "" || runID == "" || nodeID == "" {
		fs.Usage()
		return errors.New("--controller + <runID> + <nodeID> are required")
	}

	// safety: SIGINT only, and SIGTERM must stay unhandled. SIGTERM is how a
	// node process is stopped -- by `runs bounce`, by a cancelled run, by
	// a pod's own termination -- and the guarantee those paths rest on is
	// that the child dies without writing a terminal row: the supervisor
	// decides what the kill meant. A handler here would let a killed node
	// record an outcome of its own, and a bounce would land the run's
	// downstream nodes on a failure nobody asked for. See
	// TestNodeEntrypoints_DoNotHandleSIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	res, err := orchestrator.RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID,
		fmt.Sprintf("pod:%s:%s", runID, nodeID), token, orchestrator.NewJSONRenderer(), slog.Default(), nil)
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}
