package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func captureLocalRunEnvironment(ctx context.Context, home, checkout, index string, environment []string, snapshots *nodehost.FileEnvironmentSnapshots, masker *secrets.Masker) (_ runspec.EnvSnapshot, resultErr error) {
	if index == "" {
		for _, item := range environment {
			if value, ok := strings.CutPrefix(item, "SPARKWING_GATE_INDEX="); ok {
				index = value
			}
		}
	}
	if index == "" {
		return snapshots.Capture(ctx, environment)
	}
	info, err := os.Stat(index)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return runspec.EnvSnapshot{}, errors.New("run: --sw-index requires a regular Git index file")
	}
	body, err := os.ReadFile(index)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	directory := filepath.Join(home, "environments")
	temporary, err := fssecure.MkdirPrivateTemp(directory, ".index-")
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(temporary)) }()
	path := filepath.Join(temporary, "index")
	if err := fssecure.WriteFile(path, body); err != nil {
		return runspec.EnvSnapshot{}, err
	}
	gitEnv, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: environment, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	gitEnv = append(gitEnv, "GIT_INDEX_FILE="+path)
	if _, err := (triggerCheckout{directory: checkout, environment: gitEnv}).read(ctx, "update-index", "--no-split-index"); err != nil {
		return runspec.EnvSnapshot{}, err
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		return runspec.EnvSnapshot{}, err
	}
	body, err = os.ReadFile(path)
	if err != nil {
		return runspec.EnvSnapshot{}, err
	}
	retained := filepath.Join(directory, fmt.Sprintf("index-%x", sha256.Sum256(body)))
	if err := os.Rename(path, retained); err != nil {
		return runspec.EnvSnapshot{}, err
	}
	environment = slices.DeleteFunc(slices.Clone(environment), func(item string) bool { return strings.HasPrefix(item, "SPARKWING_GATE_INDEX=") })
	environment = append(environment, "SPARKWING_GATE_INDEX="+retained)
	return snapshots.Capture(ctx, environment)
}
