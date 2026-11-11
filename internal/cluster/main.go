package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// clusterRuntime supplies the shared service bindings used by every runner command.
type clusterRuntime struct {
	Runner    RunnerCLI
	Launcher  LauncherFactory
	Bootstrap BootstrapRuntime
}

// MainWithVersion runs the runner CLI with its linker-stamped release version.
func MainWithVersion(version string) {
	mainWithRuntime(version, productionRuntime(version))
}

// mainWithRuntime runs the executable with its capability, source and native host bindings.
func mainWithRuntime(version string, runtime clusterRuntime) {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "__sdk-install":
		if len(os.Args) != 2 {
			err = errors.New("SDK install accepts no arguments")
		} else {
			err = sdkinstall.Run(ctx, os.Stdin, os.Stdout, os.Stderr)
		}
	case "start", "warm":
		err = runtime.Runner.Run(ctx, os.Args[1], os.Args[2:], version)
	case "run-node":
		err = runtime.Bootstrap.Run(ctx, os.Args[1], os.Args[2:])
	case "launch":
		err = RunLaunchCLI(ctx, os.Args[2:], runtime.Launcher)
	case "version":
		err = runRunnerVersion(os.Stdout, os.Args[2:], version)
	case "-h", "--help", "help":
		usage(os.Stderr)
		return
	default:
		fmt.Fprintf(os.Stderr, "sparkwing-runner: unknown subcommand %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		if (os.Args[1] == "start" || os.Args[1] == "warm") && ctx.Err() != nil && onlyRunnerCancellation(err, context.Cause(ctx)) {
			return
		}
		writeRunnerError(os.Stderr, os.Args[1], err)
		var usage *usageError
		if errors.As(err, &usage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func onlyRunnerCancellation(err, cause error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !onlyRunnerCancellation(child, cause) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyRunnerCancellation(wrapped, cause)
	}
	return errors.Is(err, cause) || errors.Is(err, context.Canceled)
}

func writeRunnerError(writer io.Writer, command string, err error) {
	fmt.Fprintln(writer, capability.FormatLogValue(fmt.Sprint(command+": ", err)))
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: sparkwing-runner <start|warm|run-node|launch|version> [flags]")
	fmt.Fprintln(writer, "  start    - run from the machine config's runner section")
	fmt.Fprintln(writer, "  warm     - claim triggers and jobs with the shared runner")
	fmt.Fprintln(writer, "  run-node - execute a bootstrap-authorized Cloud unit")
	fmt.Fprintln(writer, "  launch   - run controller-dispatched nodes as Kubernetes Jobs (never runs pipeline code)")
	fmt.Fprintln(writer, "  version  - print this executable's offline build identity")
}

func runRunnerVersion(writer io.Writer, args []string, version string) error {
	return runRunnerVersionIO(writer, os.Stderr, args, version)
}

func runRunnerVersionIO(writer, diagnostics io.Writer, args []string, version string) error {
	fs, err := runnerflags.Parse("version", args, writer, diagnostics)
	if err != nil {
		return parsedHelp(err)
	}
	identity := buildinfo.Read("sparkwing-runner", version)
	format := clisurface.OutputFormat(textFlag(fs, "output"), color.IsInteractiveStdout())
	switch format {
	case "json":
		return json.NewEncoder(writer).Encode(identity)
	case "plain":
		fmt.Fprintln(writer, identity.Version)
		return nil
	case "pretty":
		commit := identity.Commit
		if commit == "" {
			commit = "(unknown)"
		} else if identity.Modified {
			commit += "+dirty"
		}
		fmt.Fprintf(writer, "%s %s (%s/%s, commit %s)\n",
			identity.Binary, identity.Version, identity.GOOS, identity.GOARCH, commit)
		return nil
	default:
		return fmt.Errorf("--output must be pretty, json, or plain (got %q)", format)
	}
}
