package cluster

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func Main() {
	MainWithVersion("")
}

// MainWithVersion runs the runner CLI with its linker-stamped release version.
func MainWithVersion(version string) {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "worker":
		err = runWorkerCLI(os.Args[2:])
	case "runner":
		err = runRunnerCLI(os.Args[2:])
	case "agent":
		err = runAgentCLI(os.Args[2:], buildinfo.Read("sparkwing-runner", version))
	case "wingd":
		err = orchestrator.RunWingd(os.Args[2:])
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
		os.Exit(1)
	}
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: sparkwing-runner <runner|worker|agent|version> [flags]")
	fmt.Fprintln(writer, "  runner  - long-lived warm pool pod (claims triggers + nodes)")
	fmt.Fprintln(writer, "  worker  - legacy trigger-only claim loop (prefer 'runner --also-claim-triggers')")
	fmt.Fprintln(writer, "  agent   - remote machine agent (YAML-configured, off-cluster)")
	fmt.Fprintln(writer, "  version - print this executable's offline build identity")
}

func runRunnerVersion(writer io.Writer, args []string, version string) error {
	return runRunnerVersionIO(writer, os.Stderr, args, version)
}

func runRunnerVersionIO(writer, diagnostics io.Writer, args []string, version string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	fs.Usage = func() {
		fmt.Fprintln(diagnostics, "usage: sparkwing-runner version [-o pretty|json|plain] [--offline]")
		fs.PrintDefaults()
	}
	output := fs.StringP("output", "o", "", "pretty | json | plain")
	_ = fs.Bool("offline", false, "skip network access (the runner identity never uses the network)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	identity := buildinfo.Read("sparkwing-runner", version)
	format := strings.ToLower(strings.TrimSpace(*output))
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
		return fmt.Errorf("--output must be pretty, json, or plain (got %q)", *output)
	}
}
