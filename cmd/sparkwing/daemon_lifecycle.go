package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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

func runDaemonLifecycle(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemonLifecycleCommand(ctx, inv, os.Stdout, resolveLocalServices)
}

func runDaemonLifecycleCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, resolve func(context.Context) (dashboardServices, error)) error {
	return runDaemonLifecycleCommandOn(ctx, inv, out, runtime.GOOS, resolve)
}

func runDaemonLifecycleCommandOn(ctx context.Context, inv clisurface.Invocation, out io.Writer, goos string, resolve func(context.Context) (dashboardServices, error)) error {
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
	case "sparkwing daemon install", "sparkwing daemon uninstall":
		if goos != "linux" && goos != "darwin" {
			return &apierr.Error{Code: apierr.BadRequest, Message: "controller login services are supported on Linux and macOS", Hint: "sparkwing dashboard open starts the controller on demand"}
		}
		// safety: installation waits for its successor under the client's own startup budget.
		timeout = 0
	default:
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "expected status, restart, stop, install or uninstall"}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing daemon install" {
		if err := validateServiceSecretsKeys(); err != nil {
			return err
		}
	}
	services, err := resolve(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if services.lifecycle == nil {
		return &apierr.Error{Code: hosterr.Unavailable, Message: "daemon requires local controller lifecycle"}
	}
	if inv.Command.Path == "sparkwing daemon install" || inv.Command.Path == "sparkwing daemon uninstall" {
		result, err := installDaemonService(ctx, inv, services)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		return renderDashboard(out, result, format)
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

func installDaemonService(ctx context.Context, inv clisurface.Invocation, services dashboardServices) (dashboardResult, error) {
	action := strings.TrimPrefix(inv.Command.Path, "sparkwing daemon ")
	if services.units == nil {
		return dashboardResult{}, &apierr.Error{Code: hosterr.Unavailable, Message: "daemon requires local controller managed units"}
	}
	var status wingd.LocalStatus
	var err error
	if action == "install" {
		status, err = services.lifecycle.InstallController(ctx, services.options)
	} else if err = services.units.UninstallController(ctx); err == nil {
		status, err = services.lifecycle.Status(ctx)
	}
	if err != nil {
		return dashboardResult{}, err
	}
	return localStatusResult("controller", action, status), nil
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
	if action == "status" || !status.Running && action != "stop" || action == "restart" && !inv.Bool("force") && status.Version == services.options.Version {
		return result, nil
	}
	if action == "restart" && services.options.Version == "" {
		return result, &apierr.Error{Code: hosterr.Unavailable, Message: "daemon restart requires the installed controller version"}
	}
	if action == "stop" && services.retireRunner != nil {
		if err := services.retireRunner(ctx); err != nil {
			return result, err
		}
	}
	if !status.Running {
		return result, nil
	}
	stopped, err := stopLocalController(ctx, services.lifecycle)
	if err != nil {
		return result, err
	}
	if action == "restart" {
		options := services.options
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

func validateServiceSecretsKeys() error {
	if os.Getenv("SPARKWING_SECRETS_KEY") != "" || os.Getenv("SPARKWING_SECRETS_PREVIOUS_KEY") != "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "controller login services cannot retain secrets keys supplied by value", Hint: "use SPARKWING_SECRETS_KEY_FILE instead, unset SPARKWING_SECRETS_KEY and SPARKWING_SECRETS_PREVIOUS_KEY, then install again"}
	}
	return nil
}
