package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// safety: a stalled controller must leave doctor available as a recovery tool.
const doctorDefaultTimeout = 10 * time.Second

type doctorFinding struct {
	Code    string `json:"code"`
	Unit    string `json:"unit,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type doctorReport struct {
	DryRun            bool                           `json:"dry_run"`
	ControllerChecked bool                           `json:"controller_checked"`
	ControllerError   string                         `json:"controller_error,omitempty"`
	Findings          []doctorFinding                `json:"findings"`
	GoToolchain       *opsview.DoctorGoToolchain     `json:"go_toolchain,omitempty"`
	ShadowedHooks     *githooks.Shadow               `json:"shadowed_hooks,omitempty"`
	InstallConflict   *opsview.DoctorInstallConflict `json:"install_conflict,omitempty"`
}

type doctorDependencies struct {
	version string
	local   func(context.Context) (dashboardServices, error)
	machine func(context.Context) doctorReport
	sweep   func(context.Context, dashboardServices, bool) ([]localhostwire.LocalDoctorFinding, error)
}

func runDoctor(inv clisurface.Invocation) error {
	return runDoctorCommand(context.Background(), inv, os.Stdout, doctorDependencies{
		version: installedVersion(), local: resolveLocalServices, machine: inspectDoctorMachine, sweep: sweepDoctorSessions,
	})
}

func runDoctorCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, deps doctorDependencies) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	var err error
	timeout := doctorDefaultTimeout
	if inv.Has("timeout") {
		timeout, err = time.ParseDuration(inv.Value("timeout"))
		if err != nil || timeout <= 0 {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--timeout must be positive"}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	report, inspectErr := inspectDoctor(ctx, inv.Bool("dry-run"), deps)
	return errors.Join(renderDoctor(out, report, format), inspectErr)
}

func inspectDoctor(ctx context.Context, dryRun bool, deps doctorDependencies) (doctorReport, error) {
	report := deps.machine(ctx)
	report.DryRun = dryRun
	report.Findings = []doctorFinding{}
	if err := ctx.Err(); err != nil {
		report.ControllerError = err.Error()
		return report, err
	}
	services, err := deps.local(ctx)
	if err == nil && services.lifecycle == nil {
		err = &apierr.Error{Code: apierr.Unavailable, Message: "doctor requires local controller diagnostics", Hint: "the local controller integration is unavailable in this build"}
	}
	if err != nil {
		report.ControllerError = err.Error()
		return report, err
	}
	if err := ctx.Err(); err != nil {
		report.ControllerError = err.Error()
		return report, err
	}
	// safety: lifecycle inspection must not acquire a second writing store or start an activity hold.
	findings, err := services.lifecycle.Doctor(ctx, deps.version)
	if err == nil && deps.sweep != nil {
		var swept []localhostwire.LocalDoctorFinding
		swept, err = deps.sweep(ctx, services, dryRun)
		findings = append(findings, swept...)
	}
	for _, finding := range findings {
		if strings.TrimSpace(finding.Code) == "" || strings.TrimSpace(finding.Message) == "" {
			err = errors.Join(err, &apierr.Error{Code: apierr.ProtocolError, Message: "local controller returned a finding without a code or message"})
			continue
		}
		report.Findings = append(report.Findings, doctorFinding{Code: finding.Code, Unit: finding.Unit, Message: finding.Message, Hint: finding.Hint})
	}
	err = errors.Join(err, ctx.Err())
	if err != nil {
		report.ControllerError = err.Error()
	} else {
		report.ControllerChecked = true
	}
	return report, err
}

func inspectDoctorMachine(ctx context.Context) doctorReport {
	git := func(dir string, args ...string) (string, error) { return runGitContext(ctx, dir, args...) }
	report := doctorReport{GoToolchain: diagnoseGoToolchain(ctx), ShadowedHooks: shadowedHooks(git)}
	if self, err := installsite.Self(); err == nil {
		home, _ := os.UserHomeDir()
		report.InstallConflict = opsview.InstallConflict(self, installsite.SearchDirs(os.Getenv, home))
	}
	return report
}

func renderDoctor(out io.Writer, report doctorReport, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(report)
	}
	var text bytes.Buffer
	if format == "plain" {
		fmt.Fprintf(&text, "controller_checked\t%t\n", report.ControllerChecked)
		if report.ControllerError != "" {
			fmt.Fprintf(&text, "controller_error\t%s\n", report.ControllerError)
		}
		for _, finding := range report.Findings {
			fmt.Fprintf(&text, "finding\t%s\t%s\t%s\t%s\n", finding.Code, finding.Unit, finding.Message, finding.Hint)
		}
	} else {
		if report.ControllerError != "" {
			fmt.Fprintf(&text, "Local controller checks incomplete: %s\n", report.ControllerError)
		} else if report.ControllerChecked && len(report.Findings) == 0 {
			fmt.Fprintln(&text, "No local controller findings.")
		}
		for _, finding := range report.Findings {
			fmt.Fprintf(&text, "%s [%s]: %s\n", finding.Code, emptyDash(finding.Unit), finding.Message)
			if finding.Hint != "" {
				fmt.Fprintf(&text, "  hint: %s\n", finding.Hint)
			}
		}
	}
	renderDoctorMachine(&text, report)
	_, err := out.Write(text.Bytes())
	return err
}

func renderDoctorMachine(text *bytes.Buffer, report doctorReport) {
	if toolchain := report.GoToolchain; toolchain != nil {
		fmt.Fprintf(text, "go_toolchain\t%s\t%s\n", toolchain.Current, toolchain.Verdict)
		fmt.Fprintf(text, "go_toolchain_setting\t%s\ngo_toolchain_source\t%s\ngo_toolchain_floor\t%s\ngo_toolchain_module\t%s\n", toolchain.Setting, toolchain.Source, toolchain.Floor, toolchain.Module)
		if toolchain.Override != "" {
			fmt.Fprintf(text, "go_toolchain_override\t%s\n", toolchain.Override)
		}
		if toolchain.Error != "" {
			fmt.Fprintf(text, "go_toolchain_error\t%s\n", toolchain.Error)
		}
	}
	if shadow := report.ShadowedHooks; shadow != nil {
		fmt.Fprintf(text, "shadowed_hooks\t%s\n%s\n", shadow.Summary(), shadow.Remedy())
	}
	if conflict := report.InstallConflict; conflict != nil {
		fmt.Fprintf(text, "executable\t%s\n", conflict.Self)
		for _, copy := range conflict.Competing {
			fmt.Fprintf(text, "competing_install\t%s\t%s\t%s\t%s\n", copy.Path, installsite.RetireRemedy(copy.Path).Text(), copy.Modified, copy.Resolved)
		}
	}
}

func shadowedHooks(git githooks.Git) *githooks.Shadow {
	sparkwingDir, err := findSparkwingDir()
	if err != nil {
		return nil
	}
	shadow, err := githooks.Detect(git, filepath.Dir(sparkwingDir))
	if err != nil {
		return nil
	}
	return shadow
}

func diagnoseGoToolchain(ctx context.Context) *opsview.DoctorGoToolchain {
	dir, err := findSparkwingDir()
	if err != nil {
		return nil
	}
	return inspectDoctorGoToolchain(ctx, dir, os.Environ())
}

func inspectDoctorGoToolchain(ctx context.Context, dir string, env []string) *opsview.DoctorGoToolchain {
	overlay := bincache.EffectiveOverlay(dir, os.Environ())
	report, err := gotoolchain.Inspect(ctx, dir, env, overlay)
	finding := &opsview.DoctorGoToolchain{
		Current: report.Current, Setting: report.Setting, Source: report.Source,
		Floor: report.Floor, Module: report.Module, Override: report.Override, Verdict: "ok",
	}
	if report.Override != "" {
		finding.Verdict = "sparkwing will build with " + report.Override
	}
	if err != nil {
		finding.Verdict = "unverified"
		var toolchainErr *gotoolchain.Error
		if errors.As(err, &toolchainErr) {
			finding.Verdict = "blocked by GOTOOLCHAIN=local"
		}
		finding.Error = err.Error()
	}
	return finding
}

func sweepDoctorSessions(ctx context.Context, services dashboardServices, dryRun bool) ([]localhostwire.LocalDoctorFinding, error) {
	if _, err := ensureLocalController(ctx, services.options.Home, func(context.Context) (dashboardServices, error) {
		return services, nil
	}); err != nil {
		return nil, err
	}
	socket, err := wingd.APISocketPath(services.options.Home)
	if err != nil {
		return nil, err
	}
	transport := wingdclient.NewAPIClient(socket)
	defer transport.CloseIdleConnections()
	return client.SweepLocalSessions(ctx, client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: transport}, dryRun)
}
