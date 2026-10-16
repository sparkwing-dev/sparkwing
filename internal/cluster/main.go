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
	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// Runtime supplies the shared service bindings used by every runner command.
type Runtime struct {
	Runner    RunnerCLI
	Launcher  LauncherFactory
	Bootstrap BootstrapRuntime
}

// MainWithVersion runs the runner CLI with its linker-stamped release version.
func MainWithVersion(version string) {
	MainWithRuntime(version, ProductionRuntime(version))
}

// MainWithRuntime runs the executable with its capability, source and native host bindings.
func MainWithRuntime(version string, runtime Runtime) {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "start", "warm":
		err = runtime.Runner.Run(ctx, os.Args[1], os.Args[2:], version)
	case "run-node", "fetch-source":
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
		fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
		var usage *usageError
		if errors.As(err, &usage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: sparkwing-runner <start|warm|run-node|launch|fetch-source|version> [flags]")
	fmt.Fprintln(writer, "  start    - run from the machine config's runner section")
	fmt.Fprintln(writer, "  warm     - claim triggers and jobs with the shared runner")
	fmt.Fprintln(writer, "  run-node - execute a bootstrap-authorized Cloud unit")
	fmt.Fprintln(writer, "  launch   - run controller-dispatched nodes as Kubernetes Jobs (never runs pipeline code)")
	fmt.Fprintln(writer, "  fetch-source - a launcher Job's init container: check out the run and its private modules")
	fmt.Fprintln(writer, "  version  - print this executable's offline build identity")
}

func runRunnerVersion(writer io.Writer, args []string, version string) error {
	return runRunnerVersionIO(writer, os.Stderr, args, version)
}

func runRunnerVersionIO(writer, diagnostics io.Writer, args []string, version string) error {
	fs, err := runnerflags.Parse("version", args, diagnostics)
	if err != nil {
		return parsedHelp(err)
	}
	identity := buildinfo.Read("sparkwing-runner", version)
	format := textFlag(fs, "output")
	if format == "" {
		format = "json"
		if color.IsInteractiveStdout() {
			format = "pretty"
		}
	}
	switch format {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(identity)
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
