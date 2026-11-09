package main

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type dashboardServices struct {
	runtime   wingd.LocalRuntime
	lifecycle wingd.LocalLifecycle
	units     wingd.ManagedUnitManager
	options   wingd.LocalStartOptions
	openLog   func(context.Context) (*os.File, error)
	mintLogin func(context.Context, string) (wingd.DashboardLogin, error)
	// safety: only daemon stop retires the runner; a restart must leave it and its jobs running.
	retireRunner func(context.Context) error
}

func runDashboard(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := requireDashboardBundle(inv, web.VerifyBundleEmbedded); err != nil {
		return err
	}
	return runDashboardCommand(ctx, inv, os.Stdout, runtime.GOOS, resolveLocalServices)
}

func requireDashboardBundle(inv clisurface.Invocation, verify func() error) error {
	if inv.Help() || inv.Command.Path != "sparkwing dashboard open" {
		return nil
	}
	if err := verify(); err != nil {
		return &apierr.Error{
			Code:    hosterr.Unavailable,
			Message: "this sparkwing build carries no dashboard bundle",
			Hint:    "install a release binary, or from a sparkwing checkout run bash bin/build-web.sh and then go install ./cmd/sparkwing",
		}
	}
	return nil
}

func dashboardUnavailable(dependency string) error {
	return &apierr.Error{Code: hosterr.Unavailable, Message: "dashboard requires " + dependency, Hint: "the local controller integration is unavailable in this build"}
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
	if inv.Command.Path == "sparkwing dashboard open" {
		return openDashboard(ctx, services, inv.Value("host"), out, mode)
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
	}
	return nil
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
	case "status":
		status, err = services.lifecycle.Status(ctx)
	default:
		return result, fmt.Errorf("unsupported dashboard action %q", action)
	}
	if err != nil {
		return result, err
	}
	result = localStatusResult("dashboard", action, status)
	if action == "status" {
		result.Warning = olderDashboardWarning(services.options.Home)
	}
	return result, nil
}
