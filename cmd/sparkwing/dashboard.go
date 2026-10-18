package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type dashboardServices struct {
	runtime   wingd.LocalRuntime
	lifecycle wingd.LocalLifecycle
	units     wingd.ManagedUnitManager
	options   wingd.LocalStartOptions
	openLog   func(context.Context) (*os.File, error)
}

func runDashboard(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDashboardCommand(ctx, inv, os.Stdout, runtime.GOOS, resolveLocalServices)
}

func dashboardUnavailable(dependency string) error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "dashboard requires " + dependency, Hint: "the local controller integration is unavailable in this build"}
}

func runDashboardCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, goos string, resolve func(context.Context) (dashboardServices, error)) error {
	mode := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing dashboard" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, mode)
	}
	if err := validateDashboardInvocation(inv, goos); err != nil {
		return err
	}
	services, err := resolve(ctx)
	if err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing dashboard logs" {
		return streamDashboardLogs(ctx, inv, services.openLog, out, mode)
	}
	result, err := executeDashboard(ctx, inv, services)
	if err != nil {
		return err
	}
	return renderDashboard(out, result, mode)
}

func validateDashboardInvocation(inv clisurface.Invocation, goos string) error {
	switch inv.Command.Path {
	case "sparkwing dashboard logs":
		_, err := dashboardLogLimit(inv)
		return err
	case "sparkwing dashboard install", "sparkwing dashboard uninstall":
		if goos != "linux" && goos != "darwin" {
			return &apierr.Error{Code: apierr.BadRequest, Message: "dashboard login services are supported on Linux and macOS", Hint: "use sparkwing dashboard start --stay"}
		}
		if inv.Bool("force") {
			return dashboardUnavailable("managed-unit replacement policy for --force")
		}
	case "sparkwing dashboard start":
		addr := dashboardAddress(inv)
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--addr must be HOST:PORT"}
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 0 || number > 65535 {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--addr requires a port between 0 and 65535"}
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) && !inv.Bool("allow-remote") {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "non-loopback --addr requires --allow-remote"}
		}
		if inv.Bool("read-only") || inv.Bool("allow-remote") || inv.Has("allow-origin") {
			return dashboardUnavailable("local controller dashboard access-policy options")
		}
	}
	return nil
}

func dashboardAddress(inv clisurface.Invocation) string {
	if inv.Has("addr") {
		return inv.Value("addr")
	}
	return "127.0.0.1:4343"
}

func executeDashboard(ctx context.Context, inv clisurface.Invocation, services dashboardServices) (dashboardResult, error) {
	action := strings.TrimPrefix(inv.Command.Path, "sparkwing dashboard ")
	result := dashboardResult{Kind: "service", Tool: "sparkwing", Service: "dashboard", Action: action}
	if services.lifecycle == nil {
		return result, dashboardUnavailable("local controller lifecycle")
	}
	var status wingd.LocalStatus
	var err error
	switch action {
	case "start":
		options := services.options
		options.Stay, options.DashboardAddress = inv.Bool("stay"), dashboardAddress(inv)
		status, err = services.lifecycle.StartDashboard(ctx, options)
	case "stop":
		status, err = stopLocalController(ctx, services.lifecycle)
	case "status":
		status, err = services.lifecycle.Status(ctx)
	case "install", "uninstall":
		if services.units == nil {
			return result, dashboardUnavailable("local controller managed units")
		}
		if action == "install" {
			options := services.options
			options.Stay = true
			status, err = services.lifecycle.InstallController(ctx, options)
		} else {
			err = services.units.UninstallController(ctx)
		}
		if err == nil && action == "uninstall" {
			status, err = services.lifecycle.Status(ctx)
		}
	default:
		return result, fmt.Errorf("unsupported dashboard action %q", action)
	}
	if err != nil {
		return result, err
	}
	return localStatusResult("dashboard", action, status), nil
}
