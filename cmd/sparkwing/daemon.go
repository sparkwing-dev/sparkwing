package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

type daemonReport struct {
	Running          bool   `json:"running"`
	Healthy          bool   `json:"healthy"`
	Draining         bool   `json:"draining"`
	Restarted        bool   `json:"restarted"`
	BinaryVersion    string `json:"binary_version,omitempty"`
	RunningRevision  string `json:"running_revision,omitempty"`
	PreviousVersion  string `json:"previous_version,omitempty"`
	PreviousRevision string `json:"previous_revision,omitempty"`
	Socket           string `json:"socket"`
}

func runDaemon(args []string) error {
	if handleParentHelp(cmdDaemon, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDaemon, os.Stdout)
		return nil
	}
	switch args[0] {
	case "status":
		return runDaemonStatus(args[1:])
	case "restart":
		return runDaemonRestart(args[1:])
	default:
		PrintHelp(cmdDaemon, os.Stderr)
		return fmt.Errorf("daemon: unknown subcommand %q", args[0])
	}
}

func runDaemonStatus(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonStatus.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing home to inspect")
	if err := parseAndCheck(cmdDaemonStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := inspectDaemon(ctx, *home)
	if err != nil {
		return err
	}
	return emitDaemonReport(report, *output)
}

func inspectDaemon(ctx context.Context, home string) (daemonReport, error) {
	socket, err := wingd.SocketPath(home)
	if err != nil {
		return daemonReport{}, err
	}
	report := daemonReport{Socket: socket}
	info, err := wingdclient.Probe(ctx, socket)
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		return report, nil
	}
	if err != nil {
		return daemonReport{}, fmt.Errorf("daemon status: %w", err)
	}
	report.Running = true
	report.Healthy = !info.Draining
	report.Draining = info.Draining
	report.BinaryVersion = info.BinaryVersion
	report.RunningRevision = versionRevision(info.BinaryVersion)
	return report, nil
}

func runDaemonRestart(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonRestart.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing home to refresh")
	if err := parseAndCheck(cmdDaemonRestart, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target := installedVersion()
	result, err := wingdclient.RefreshRunning(ctx, wingdclient.Options{
		Home:    *home,
		Version: target,
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		report, inspectErr := inspectDaemon(ctx, *home)
		if inspectErr != nil {
			return inspectErr
		}
		return emitDaemonReport(report, *output)
	}
	if err != nil {
		return fmt.Errorf("daemon restart: %w", err)
	}
	report, err := inspectDaemon(ctx, *home)
	if err != nil {
		return err
	}
	report.Restarted = result.Restarted
	report.PreviousVersion = result.PreviousVersion
	report.PreviousRevision = versionRevision(result.PreviousVersion)
	return emitDaemonReport(report, *output)
}

func emitDaemonReport(report daemonReport, output string) error {
	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "plain":
		if !report.Running {
			fmt.Fprintln(os.Stdout, "stopped")
			return nil
		}
		fmt.Fprintln(os.Stdout, report.BinaryVersion)
		return nil
	case "pretty", "":
		if !report.Running {
			fmt.Fprintln(os.Stdout, "wingd is stopped")
			return nil
		}
		action := "running"
		if report.Restarted {
			action = "restarted"
		}
		fmt.Fprintf(os.Stdout, "wingd %s %s\n", action, report.BinaryVersion)
		return nil
	default:
		return fmt.Errorf("daemon: unsupported output format %q", output)
	}
}
