package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type upgradeResolver interface {
	Upgrade(context.Context, engine.UpgradeRequest, engine.UpgradeVerifier) (engine.UpgradeResult, error)
}

type upgradeCommand struct {
	newResolver func() (upgradeResolver, error)
	verifier    engine.UpgradeVerifier
	out         io.Writer
	terminal    bool
}

func runUpgrade(inv clisurface.Invocation) error {
	command := upgradeCommand{
		newResolver: func() (upgradeResolver, error) {
			mode, err := toolchain.ParseMode(os.Getenv("SPARKWING_TOOLCHAIN"))
			if err != nil {
				return nil, err
			}
			return engine.NewResolver(engine.Config{BuildVersion: installedVersion(), Mode: mode})
		},
		verifier: &upgradeVerifier{environment: os.Environ(), buildEnv: procenv.Build, exec: executeUpgradeCheck},
		out:      os.Stdout, terminal: color.IsInteractiveStdout(),
	}
	return command.run(context.Background(), inv)
}

func (c upgradeCommand) run(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), c.out, inv.Output(c.terminal))
	}
	start, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err := findSparkwingDirFrom(start)
	if err != nil {
		return err
	}
	typescript, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return err
	}
	sdk := engineid.GoSDK
	if typescript {
		sdk = engineid.TypeScriptSDK
	}
	resolver, err := c.newResolver()
	if err != nil {
		return err
	}
	request := engine.UpgradeRequest{Checkout: filepath.Dir(dir), SDK: sdk, Version: inv.Value("version"), DryRun: inv.Bool("dry-run")}
	result, err := resolver.Upgrade(ctx, request, c.verifier)
	if err != nil {
		return err
	}
	if result.DryRun != request.DryRun || result.DryRun && (result.Applied || result.Verified) || !result.DryRun && result.Verified == (result.Unverified != nil) {
		return errors.New("upgrade: resolver returned an inconsistent verification result")
	}
	if !result.DryRun && len(result.Plan.Edits) > 0 && !result.Applied {
		return errors.New("upgrade: pin changes were not applied")
	}
	if err := writeUpgradeReport(c.out, inv.Output(c.terminal), result); err != nil {
		return err
	}
	if result.Unverified != nil {
		return &apierr.Error{
			Code:    apierr.PlanFailed,
			Message: fmt.Sprintf("the SDK pin moved to %s, but the pipeline does not build or plan against it yet: %v", result.Plan.After.Version, result.Unverified),
			Hint:    "Make the code changes the migration guides describe, " + engine.MigrationGuides(result.Plan.Before.Version, result.Plan.After.Version) + ", then rerun the pipeline. `git checkout -- .sparkwing` returns to the old pin.",
		}
	}
	return nil
}

type upgradeReport struct {
	Before   engineid.Pin      `json:"before"`
	After    engineid.Pin      `json:"after"`
	Engine   engineid.Identity `json:"engine"`
	Files    []string          `json:"files"`
	Notices  []engineid.Notice `json:"notices"`
	Applied  bool              `json:"applied"`
	Verified bool              `json:"verified"`
	DryRun   bool              `json:"dry_run"`
}

func writeUpgradeReport(out io.Writer, format string, result engine.UpgradeResult) error {
	report := upgradeReport{Before: result.Plan.Before, After: result.Plan.After, Engine: result.Plan.Engine, Files: []string{}, Notices: result.Plan.Notices, Applied: result.Applied, Verified: result.Verified, DryRun: result.DryRun}
	if report.Notices == nil {
		report.Notices = []engineid.Notice{}
	}
	for _, edit := range result.Plan.Edits {
		report.Files = append(report.Files, edit.Path)
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(report)
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, report.After.Version)
		return err
	}
	status := "Verified"
	if result.Applied {
		status = "Verified and applied"
	}
	if result.Applied && !result.Verified {
		status = "Applied, pipeline code needs changes"
	}
	if result.DryRun {
		status = "Preview"
	}
	if _, err := fmt.Fprintf(out, "%s: %s SDK %s -> %s\n", status, report.After.SDK, report.Before.Version, report.After.Version); err != nil {
		return err
	}
	for _, path := range report.Files {
		if _, err := fmt.Fprintln(out, "  "+path); err != nil {
			return err
		}
	}
	return writeEngineNotices(out, report.Notices)
}
