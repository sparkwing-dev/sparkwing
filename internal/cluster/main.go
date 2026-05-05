package cluster

import (
	"fmt"
	"os"
)

// Main is the entry point for the sparkwing-runner binary
// (cmd/sparkwing-runner/main.go). Dispatches the cluster-side
// subcommands that an operator or the K8s Deployment runs:
func Main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "worker":
		err = runWorkerCLI(os.Args[2:])
	case "runner":
		err = runRunnerCLI(os.Args[2:])
	case "agent":
		err = RunAgentCLI(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "sparkwing-runner: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sparkwing-runner <runner|worker|agent> [flags]")
	fmt.Fprintln(os.Stderr, "  runner  - long-lived warm pool pod (claims triggers + nodes)")
	fmt.Fprintln(os.Stderr, "  worker  - legacy trigger-only claim loop (prefer 'runner --also-claim-triggers')")
	fmt.Fprintln(os.Stderr, "  agent   - laptop agent (YAML-configured, off-cluster)")
}
