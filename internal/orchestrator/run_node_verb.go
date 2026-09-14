package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// RunNodeCommand executes a single claimed node from the command line and
// returns the node's failure, if any. Both the sparkwing CLI and
// sparkwing-runner expose it as their `run-node` verb, so a Kubernetes Job can
// invoke either binary:
//
//	RunNodeCommand([]string{"--controller", "https://controller", runID, nodeID})
//
// The run and node ids may come from the positional arguments or from
// SPARKWING_RUN_ID and SPARKWING_NODE_ID.
func RunNodeCommand(args []string) error {
	fs := flag.NewFlagSet("run-node", flag.ExitOnError)
	controllerURL := fs.String("controller", ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"), "controller base URL")
	logsURL := fs.String("logs", ResolveDevEnvURL("SPARKWING_LOGS_URL"), "logs-service URL")
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
	var runOpts []RunNodeOption
	if apiSocket != "" {
		runOpts = append(runOpts, OverAPISocket(apiSocket))
		token = ""
	}
	res, err := RunNodeOnce(ctx, *controllerURL, *logsURL, runID, nodeID,
		fmt.Sprintf("pod:%s:%s", runID, nodeID), token, NewJSONRenderer(), slog.Default(), nil, runOpts...)
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}
