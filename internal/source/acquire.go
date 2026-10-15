package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func acquire(ctx context.Context, spec Spec, options Options) (entry *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Engines == nil || options.Installer == nil || options.InstallEnvironment == nil {
		return nil, errors.New("source acquisition requires an engine resolver, entry installer and explicit install environment")
	}
	if options.Target.Binary != releaseasset.Sparkwing {
		return nil, errors.New("source acquisition requires the SDK-matched sparkwing engine")
	}
	if _, err := options.Target.Name(); err != nil {
		return nil, err
	}
	entry, err = PrepareCheckout(ctx, spec, options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, entry.Close())
			entry = nil
		}
	}()
	entry.Engine, err = resolveEngine(ctx, spec, options, entry.Directory)
	if err != nil {
		return entry, err
	}
	command, err := options.Installer.Install(ctx, entry.Engine, entry.Directory, append([]string{}, options.InstallEnvironment...))
	if err != nil {
		return entry, fmt.Errorf("install source entry: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return entry, err
	}
	if len(command) == 0 || command[0] == "" {
		return entry, errors.New("source installer returned no SDK command")
	}
	for _, arg := range command {
		if strings.ContainsRune(arg, '\x00') {
			return entry, errors.New("source installer returned an invalid SDK command")
		}
	}
	entry.Command = append([]string{}, command...)
	return entry, nil
}

func resolveEngine(ctx context.Context, spec Spec, options Options, directory string) (engine.Resolved, error) {
	readPin := options.ReadPin
	if readPin == nil {
		readPin = engine.ReadPin
	}
	pin, err := readPin(directory, options.SDK)
	if err != nil {
		return engine.Resolved{}, fmt.Errorf("read source SDK pin: %w", err)
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
