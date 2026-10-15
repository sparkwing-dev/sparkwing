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
	config                       NativeInstallationConfig
	mu                           sync.Mutex
	drained, replaced, refreshed bool
	version                      string
}

func (i *nativeInstallation) Drain(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.drained, i.replaced, i.refreshed = false, false, false
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.config.Drain(ctx); err != nil {
		return err
	}
	i.drained = true
	return nil
}

func (i *nativeInstallation) Replace(ctx context.Context, release engine.VerifiedRelease) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.drained {
		return errors.New("runner must finish draining before replacement")
	}
	i.replaced, i.refreshed = false, false
	if err := ctx.Err(); err != nil {
		return err
	}
	target := releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if release.Target() != target || release.Version() == "" || release.Asset().Name() == "" {
		return errors.New("replacement must be a verified runner release for this machine")
	}
	path, err := filepath.EvalSymlinks(i.config.Executable)
	if err != nil {
		return fmt.Errorf("resolve installed runner: %w", err)
	}
	if err := installRunner(ctx, path, release.Asset()); err != nil {
		return err
	}
	i.replaced = true
	i.version = release.Version()
	return nil
}

func (i *nativeInstallation) RefreshManagedUnits(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.replaced {
		return errors.New("runner replacement must succeed before refreshing managed units")
	}
	i.refreshed = false
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.config.RefreshManagedUnits(ctx, i.version); err != nil {
		return err
	}
	i.refreshed = true
	return nil
}

func (i *nativeInstallation) Restart(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.refreshed {
		return errors.New("managed-unit refresh must succeed before restarting the runner")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.config.Restart(ctx); err != nil {
		return err
	}
	i.drained, i.replaced, i.refreshed = false, false, false
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
