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
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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
	apiSocket := os.Getenv(wingwire.APISocketEnv)
	if (*controllerURL == "" && apiSocket == "") || runID == "" || nodeID == "" {
		fs.Usage()
		return errors.New("--controller (or " + wingwire.APISocketEnv + ") + <runID> + <nodeID> are required")
	}

	// safety: leave SIGTERM unhandled; bounce, cancellation, and pod termination
	// rely on the supervisor rather than the killed node to record the outcome.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	token := os.Getenv("SPARKWING_AGENT_TOKEN")
	var runOpts []orchestrator.RunNodeOption
	if apiSocket != "" {
		runOpts = append(runOpts, orchestrator.OverAPISocket(apiSocket))
		token = ""
	}
	res, err := orchestrator.RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID,
		fmt.Sprintf("pod:%s:%s", runID, nodeID), token, orchestrator.NewJSONRenderer(), slog.Default(), nil, runOpts...)
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}
