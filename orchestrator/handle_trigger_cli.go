package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"
)

// runHandleTriggerCLI handles `wing handle-trigger <id> [flags]`.
// Adopts an already-claimed trigger and runs it to terminal state.
// --local skips the controller and uses LocalBackends.
func runHandleTriggerCLI(args []string) error {
	fs := flag.NewFlagSet("handle-trigger", flag.ExitOnError)
	controllerURL := fs.String("controller", ResolveDevEnvURL("SPARKWING_CONTROLLER_URL"),
		"controller URL (env: SPARKWING_CONTROLLER_URL, falls back to $SPARKWING_HOME/dev.env)")
	logsURL := fs.String("logs", ResolveDevEnvURL("SPARKWING_LOGS_URL"),
		"logs service URL (env: SPARKWING_LOGS_URL, falls back to $SPARKWING_HOME/dev.env)")
	token := fs.String("token", os.Getenv("SPARKWING_AGENT_TOKEN"),
		"bearer token for controller + logs calls (env: SPARKWING_AGENT_TOKEN)")
	heartbeat := fs.Duration("heartbeat", 5*time.Second,
		"heartbeat cadence for the claim lease (cluster mode only)")
	local := fs.Bool("local", false,
		"run against the laptop SQLite store; no controller required")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		return errors.New("usage: handle-trigger <trigger-id> [--controller URL --token T | --local]")
	}
	triggerID := fs.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *local {
		if err := HandleClaimedTriggerLocal(ctx, triggerID); err != nil {
			return fmt.Errorf("handle %s (local): %w", triggerID, err)
		}
		return nil
	}

	if *controllerURL == "" {
		return errors.New("--controller (or SPARKWING_CONTROLLER_URL) required (or pass --local)")
	}
	opts := WorkerOptions{
		ControllerURL:     *controllerURL,
		LogsURL:           *logsURL,
		Token:             *token,
		HeartbeatInterval: *heartbeat,
	}
	if err := HandleClaimedTrigger(ctx, opts, triggerID); err != nil {
		return fmt.Errorf("handle %s: %w", triggerID, err)
	}
	return nil
}
