package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func acquire(ctx context.Context, spec Spec, options Options) (entry *Entry, err error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if options.Engines == nil {
		return nil, errors.New("source acquisition requires an engine resolver")
	}
	if options.Target.Binary != releaseasset.Sparkwing {
		return nil, errors.New("source acquisition requires the SDK-matched sparkwing engine")
	}
	if _, err := options.Target.Name(); err != nil {
		return nil, err
	}
	entry, err = PrepareCheckout(ctx, spec, options)
	if err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	defer func() {
		if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
		if err != nil {
			err = errors.Join(err, entry.Close())
			entry = nil
		}
	}()
	entry.Engine, err = resolveEngine(ctx, spec, options, entry.Directory)
	if err != nil {
		return entry, err
	}
	if err := context.Cause(ctx); err != nil {
		return entry, err
	}
	environment, err := sourceEnvironment(options, procenv.Install)
	if err != nil {
		return entry, err
	}
	install := options.InstallSDK
	if install == nil {
		install = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			return selected.InstallSDK(ctx, request)
		}
	}
	relative, err := filepath.Rel(entry.checkoutDirectory, entry.Directory)
	if err != nil {
		return entry, err
	}
	command, err := install(ctx, entry.Engine, engine.SDKInstallRequest{
		Directory: entry.checkoutDirectory, PipelineDir: relative, SDK: options.SDK, Environment: environment,
	})
	if err != nil {
		return entry, fmt.Errorf("install source entry: %w", err)
	}
	if err := context.Cause(ctx); err != nil {
		return entry, err
	}
	if command.Directory != entry.Directory || len(command.Command) == 0 || command.Command[0] == "" {
		return entry, errors.New("source installer returned no SDK command")
	}
	for _, arg := range command.Command {
		if strings.ContainsRune(arg, '\x00') {
			return entry, errors.New("source installer returned an invalid SDK command")
		}
	}
	entry.Command = append([]string{}, command.Command...)
	return entry, nil
}

func resolveEngine(ctx context.Context, spec Spec, options Options, directory string) (engine.Resolved, error) {
	if err := context.Cause(ctx); err != nil {
		return engine.Resolved{}, err
	}
	readPin := options.ReadPin
	if readPin == nil {
		readPin = engine.ReadPin
	}
	pin, err := readPin(directory, options.SDK)
	if err != nil {
		return engine.Resolved{}, fmt.Errorf("read source SDK pin: %w", err)
	}
	if err := context.Cause(ctx); err != nil {
		return engine.Resolved{}, err
	}
	var resolved engine.Resolved
	if spec.Engine == nil {
		resolved, err = options.Engines.Resolve(ctx, pin, options.Target)
	} else {
		if spec.Engine.Pin != pin {
			return engine.Resolved{}, errors.New("source SDK pin differs from the sealed engine")
		}
		resolved, err = options.Engines.Ensure(ctx, *spec.Engine, options.Target)
	}
	if err != nil {
		return engine.Resolved{}, fmt.Errorf("resolve source engine: %w", err)
	}
	if resolved.Identity.Pin != pin || resolved.Target != options.Target || !filepath.IsAbs(resolved.Executable) || resolved.Identity.ManifestDigest == "" {
		return engine.Resolved{}, errors.New("source engine resolution returned an incomplete or mismatched identity")
	}
	if spec.Engine != nil && resolved.Identity != *spec.Engine {
		return engine.Resolved{}, errors.New("source engine resolution differs from the sealed engine")
	}
	return resolved, nil
}

func sourceEnvironment(options Options, kind procenv.Kind) ([]string, error) {
	build := options.BuildEnvironment
	if build == nil {
		build = procenv.Build
	}
	env, err := build(kind, options.Base, procenv.Runtime{})
	if err != nil {
		return nil, fmt.Errorf("source process environment: %w", err)
	}
	return append([]string{}, env...), nil
}
