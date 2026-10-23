package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

const updateRunnerServicesNote = "Installed runner services update themselves from their controller."

type updateCommand struct {
	out      io.Writer
	terminal bool
	check    func(string, bool, bool) updateCheckReport
	install  func(string, bool, bool) (updateReceipt, error)
	units    func(context.Context) (wingd.ManagedUnitManager, error)
}

func runUpdate(inv clisurface.Invocation) error {
	command := updateCommand{
		out: os.Stdout, terminal: color.IsInteractiveStdout(), install: updateBinary,
		check: func(version string, force, override bool) updateCheckReport {
			return gatherUpdateCheck("cli", version, force, override)
		},
		units: resolveUpdateUnits,
	}
	return command.run(context.Background(), inv)
}

func resolveUpdateUnits(ctx context.Context) (wingd.ManagedUnitManager, error) {
	services, err := resolveLocalServices(ctx)
	return services.units, err
}

func (c updateCommand) run(ctx context.Context, inv clisurface.Invocation) error {
	mode := inv.Output(c.terminal)
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), c.out, mode)
	}
	if inv.Has("version") && !validUpdateVersion(inv.Value("version")) {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--version must be a canonical release tag such as v0.48.1; omit it for latest"}
	}
	if inv.Bool("dry-run") {
		return renderUpdateCheck(c.out, c.check(inv.Value("version"), inv.Bool("force"), inv.Bool("override-hold")), mode)
	}
	units, err := c.units(ctx)
	if err != nil {
		return err
	}
	if units == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "update requires managed-unit integration"}
	}
	result, err := c.install(inv.Value("version"), inv.Bool("force"), inv.Bool("override-hold"))
	if err != nil {
		return err
	}
	if err := validateManagedUpdate(result); err != nil {
		return err
	}
	if err := units.Refresh(ctx, result.After.Path, result.After.Version); err != nil {
		return fmt.Errorf("CLI %s is installed at %s, but managed-unit refresh failed: %w; run sparkwing doctor, then sparkwing update --version %s to retry", result.After.Version, result.After.Path, err, result.After.Version)
	}
	result.ManagedUnits = "refreshed"
	if result.Status == "updated" {
		result.RunnerServices = updateRunnerServicesNote
	}
	return writeUpdateReceipt(c.out, result, mode)
}

func validateManagedUpdate(result updateReceipt) error {
	if result.Target != "cli" || result.Status != "current" && result.Status != "updated" ||
		!filepath.IsAbs(result.After.Path) || !validUpdateVersion(result.After.Version) ||
		result.Status == "updated" && (!isSHA256Digest(result.Digest) || result.ResolvedRelease != result.After.Version) {
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "installed CLI identity is unconfirmed; managed units were not refreshed", Hint: "inspect the installed binary with sparkwing doctor"}
	}
	return nil
}
