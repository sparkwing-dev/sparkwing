package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

const (
	daemonRestartTimeout = 20 * time.Second
	daemonStopTimeout    = 30 * time.Second
)

type daemonLifecycleResult struct {
	dashboardResult
	Restarted       bool   `json:"restarted"`
	Stopped         bool   `json:"stopped"`
	PreviousVersion string `json:"previous_version,omitempty"`
}

func runDaemonLifecycle(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemonLifecycleCommand(ctx, args, os.Stdout, resolveLocalServices)
}

func runDaemonLifecycleCommand(ctx context.Context, args []string, out io.Writer, resolve func(context.Context) (dashboardServices, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"daemon"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	timeout := 2 * time.Second
	switch inv.Command.Path {
	case "sparkwing daemon status":
	case "sparkwing daemon restart":
		timeout = daemonRestartTimeout
	case "sparkwing daemon stop":
		timeout = daemonStopTimeout
	default:
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "expected status, restart or stop"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	services, err := resolve(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if services.lifecycle == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "daemon requires local controller lifecycle"}
	}
	result, err := executeDaemonLifecycle(ctx, inv, services)
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	return renderDashboard(out, result.dashboardResult, format)
}

func executeDaemonLifecycle(ctx context.Context, inv clisurface.Invocation, services dashboardServices) (daemonLifecycleResult, error) {
	action := strings.TrimPrefix(inv.Command.Path, "sparkwing daemon ")
	status, err := services.lifecycle.Status(ctx)
	result := daemonLifecycleResult{dashboardResult: localStatusResult("controller", action, status)}
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if action == "status" || !status.Running || action == "restart" && !inv.Bool("force") && status.Version == services.options.Version {
		return result, nil
	}
	if action == "restart" && services.options.Version == "" {
		return result, &apierr.Error{Code: apierr.Unavailable, Message: "daemon restart requires the installed controller version"}
	}
	stopped, err := stopLocalController(ctx, services.lifecycle)
	if err != nil {
		return result, err
	}
	if action == "restart" {
		options := services.options
		options.Stay = status.Stay
		services.options = options
		stopped, err = ensureLocalController(ctx, options.Home, func(context.Context) (dashboardServices, error) {
			return services, nil
		})
		if err != nil {
			return result, err
		}
		if !stopped.Running || stopped.Version != options.Version {
			return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller restart did not establish the installed version"}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.dashboardResult = localStatusResult("controller", action, stopped)
	result.PreviousVersion = status.Version
	result.Restarted, result.Stopped = action == "restart", action == "stop"
	return result, nil
}
