package cluster

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

// BootstrapOptions contains launcher-owned paths and the award's immutable fence.
// The host composition reads the private bootstrap once and keeps its exchange in memory.
type BootstrapOptions struct {
	ControllerURL string
	RunID         string
	JobID         string
	Generation    int64
	BootstrapFile string
	ModuleCache   string
}

// BootstrapRuntime binds the private Cloud award to shared hosting.
type BootstrapRuntime struct {
	Host func(context.Context, BootstrapOptions) (launcher.BootstrapHost, func() error, error)
}

// Run shares the builder's grammar and checks its fence before constructing host services.
func (b BootstrapRuntime) Run(ctx context.Context, command string, args []string) error {
	if command != "run-node" {
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
	options := BootstrapOptions{ControllerURL: textFlag(fs, "controller"), RunID: textFlag(fs, "run"), JobID: textFlag(fs, "job"), Generation: generation, BootstrapFile: textFlag(fs, "bootstrap-file"), ModuleCache: textFlag(fs, "module-cache")}
	return b.run(ctx, command, options)
}

func (b BootstrapRuntime) run(ctx context.Context, command string, options BootstrapOptions) (err error) {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if !filepath.IsAbs(options.ModuleCache) {
		return errors.New("cloud bootstrap requires an absolute module-cache path")
	}
	award, err := launcher.ReadBootstrapAward(options.BootstrapFile)
	if err != nil {
		return err
	}
	if string(award.Binding.RunID) != options.RunID || string(award.Binding.NodeID) != options.JobID || int64(award.Binding.Generation) != options.Generation {
		return errors.New("Cloud bootstrap arguments differ from the private award")
	}
	if command != "run-node" || b.Host == nil {
		return errors.New("Cloud bootstrap requires the shared host service binding")
	}
	host, closeBinding, err := b.Host(ctx, options)
	defer func() {
		if closeBinding != nil {
			err = errors.Join(err, closeBinding())
		}
	}()
	if err != nil {
		return err
	}
	host.ControllerURL = options.ControllerURL
	return host.Run(ctx, award)
}
