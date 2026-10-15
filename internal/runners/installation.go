package runners

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// NativeInstallationConfig binds replacement and lifecycle hooks to the installed runner.
type NativeInstallationConfig struct {
	Executable          string
	Drain               func(context.Context) error
	RefreshManagedUnits func(context.Context, string) error
	Restart             func(context.Context) error
}

// NewNativeInstallation replaces the resolved executable while preserving its path and permissions.
func NewNativeInstallation(config NativeInstallationConfig) (RunnerInstallation, error) {
	if !filepath.IsAbs(config.Executable) || config.Drain == nil || config.RefreshManagedUnits == nil || config.Restart == nil {
		return nil, errors.New("native runner installation requires an absolute executable path and drain, managed-unit refresh and restart hooks")
	}
	return &nativeInstallation{config: config}, nil
}

type nativeInstallation struct {
	config NativeInstallationConfig
	mu     sync.Mutex
}

func (i *nativeInstallation) Apply(ctx context.Context, release engine.VerifiedRelease) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	target := releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if release.Target() != target || release.Version() == "" || release.Asset().Name() == "" {
		return errors.New("replacement must be a verified runner release for this machine")
	}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"drain held claims", i.config.Drain},
		{"replace executable", func(ctx context.Context) error {
			path, err := filepath.EvalSymlinks(i.config.Executable)
			if err != nil {
				return fmt.Errorf("resolve installed runner: %w", err)
			}
			return installRunner(ctx, path, release.Asset())
		}},
		{"refresh existing services and schedules", func(ctx context.Context) error { return i.config.RefreshManagedUnits(ctx, release.Version()) }},
		{"restart registered runner", i.config.Restart},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("runner update could not %s: %w; preserve the existing configuration and registration, then retry the update", step.name, err)
		}
	}
	return nil
}

func installRunner(ctx context.Context, path string, asset releaseasset.Verified) (err error) {
	current, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect installed runner: %w", err)
	}
	if !current.Mode().IsRegular() {
		return errors.New("installed runner must be a regular executable file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read installed runner: %w", err)
	}
	if runnerDigest(body) == asset.Digest() {
		return nil
	}
	stage, err := os.CreateTemp(filepath.Dir(path), ".sparkwing-runner-update-*"+filepath.Ext(path))
	if err != nil {
		return fmt.Errorf("stage runner replacement: %w", err)
	}
	defer func() {
		_ = stage.Close()
		if cleanupErr := os.Remove(stage.Name()); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove staged runner: %w", cleanupErr))
		}
	}()
	if _, err := stage.Write(asset.Bytes()); err != nil {
		return fmt.Errorf("write staged runner: %w", err)
	}
	if err := stage.Chmod(current.Mode().Perm()); err != nil {
		return fmt.Errorf("preserve runner permissions: %w", err)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync staged runner: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close staged runner: %w", err)
	}
	body, err = os.ReadFile(stage.Name())
	if err != nil {
		return fmt.Errorf("read staged runner: %w", err)
	}
	if runnerDigest(body) != asset.Digest() {
		return errors.New("staged runner does not match its verified release digest")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceInstalledRunner(stage.Name(), path); err != nil {
		return fmt.Errorf("replace installed runner: %w", err)
	}
	return nil
}

func runnerDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
