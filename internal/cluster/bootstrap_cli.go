package cluster

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// BootstrapOptions contains launcher-owned paths and the award's immutable fence.
// The host composition reads the private bootstrap once and keeps its exchange in memory.
type BootstrapOptions struct {
	ControllerURL string
	RunID         string
	JobID         string
	Generation    int64
	BootstrapFile string
	SourceDir     string
	ModuleCache   string
}

// BootstrapHost composes authorized source acquisition or host execution with
// the shared bootstrap exchange. The CLI never reconstructs source from env.
type BootstrapHost func(context.Context, BootstrapOptions) error

// RunBootstrapCLI keeps both pod entrypoints on the declared bootstrap grammar.
func RunBootstrapCLI(ctx context.Context, command string, args []string, host BootstrapHost) error {
	if command != "run-node" && command != "fetch-source" {
		return fmt.Errorf("unknown bootstrap command %q", command)
	}
	fs, err := commandFlags(command, args)
	if err != nil {
		return parsedHelp(err)
	}
	generation, err := strconv.ParseInt(textFlag(fs, "generation"), 10, 64)
	if err != nil || generation < 1 {
		return errors.New("--generation must be a positive integer")
	}
	options := BootstrapOptions{ControllerURL: textFlag(fs, "controller"), RunID: textFlag(fs, "run"), JobID: textFlag(fs, "job"), Generation: generation, BootstrapFile: textFlag(fs, "bootstrap-file"), SourceDir: textFlag(fs, "source-dir"), ModuleCache: textFlag(fs, "module-cache")}
	if host == nil {
		return errors.New(command + " runtime is unavailable: compose the authorized source, bootstrap exchange and host library")
	}
	return host(ctx, options)
}
