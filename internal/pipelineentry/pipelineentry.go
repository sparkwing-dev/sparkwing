// Package pipelineentry says how a host starts a repository's pipeline as an
// SDK process: a compiled Go binary, or node running a TypeScript entry behind
// the SDK's bootstrap preload. A .sparkwing whose package.json depends on
// @sparkwing/sdk is TypeScript; any other is a Go module.
package pipelineentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
)

// ProtocolArg is the one argument a host appends to an entry's command.
const ProtocolArg = "--sw-node-protocol"

// SDKPackage is the npm package a TypeScript pipeline depends on.
const SDKPackage = "@sparkwing/sdk"

// ErrNotInstalled reports a TypeScript pipeline whose dependencies are not
// installed in .sparkwing/node_modules.
var ErrNotInstalled = errors.New("the TypeScript pipeline's dependencies are not installed")

// Command is the program and the arguments a host runs, with [ProtocolArg]
// appended, to start one SDK process.
type Command []string

// Binary is the command for a compiled pipeline binary.
func Binary(path string) Command { return Command{path} }

// Node is the command for a TypeScript entry: the bootstrap is preloaded
// with --import so it scrubs the node bearer before the entry's imports run.
func Node(node, bootstrap, entry string) Command {
	path := filepath.ToSlash(bootstrap)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return Command{node, "--import", (&url.URL{Scheme: "file", Path: path}).String(), entry}
}

// Exec returns the command that starts the SDK process.
func (c Command) Exec() *exec.Cmd {
	// #nosec G204 -- the pipeline entry the host resolved, run as argv without a shell.
	return exec.Command(c[0], append(slices.Clone(c[1:]), ProtocolArg)...)
}

type packageJSON struct {
	Main            string                     `json:"main"`
	Dependencies    map[string]string          `json:"dependencies"`
	DevDependencies map[string]string          `json:"devDependencies"`
	Exports         map[string]json.RawMessage `json:"exports"`
}

func readPackage(path string) (packageJSON, error) {
	var pkg packageJSON
	raw, err := os.ReadFile(path) // #nosec G304 -- a package.json under the pipeline directory.
	if err != nil {
		return pkg, err
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return pkg, fmt.Errorf("%s: %w", path, err)
	}
	return pkg, nil
}

// IsTypeScript reports whether sparkwingDir holds a TypeScript pipeline: a
// package.json that depends on [SDKPackage].
func IsTypeScript(sparkwingDir string) (bool, error) {
	pkg, err := readPackage(filepath.Join(sparkwingDir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, dep := pkg.Dependencies[SDKPackage]
	_, dev := pkg.DevDependencies[SDKPackage]
	return dep || dev, nil
}

// TypeScript returns the command for the TypeScript pipeline in sparkwingDir.
// It wraps [ErrNotInstalled] when node_modules lacks the SDK.
func TypeScript(sparkwingDir string) (Command, error) {
	pkg, err := readPackage(filepath.Join(sparkwingDir, "package.json"))
	if err != nil {
		return nil, err
	}
	if pkg.Main == "" {
		return nil, fmt.Errorf("%s names no \"main\" pipeline entry", filepath.Join(sparkwingDir, "package.json"))
	}
	entry := filepath.Join(sparkwingDir, filepath.FromSlash(pkg.Main))
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("pipeline entry: %w", err)
	}
	sdkDir := filepath.Join(sparkwingDir, "node_modules", filepath.FromSlash(SDKPackage))
	sdk, err := readPackage(filepath.Join(sdkDir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has no %s", ErrNotInstalled, sparkwingDir, SDKPackage)
	}
	if err != nil {
		return nil, err
	}
	var rel string
	if err := json.Unmarshal(sdk.Exports["./bootstrap"], &rel); err != nil || rel == "" {
		return nil, fmt.Errorf("the installed %s exports no ./bootstrap preload; install an SDK that matches this engine", SDKPackage)
	}
	// safety: the preload must be the very module the SDK imports, and Node.js
	// names a module by its resolved path, so a linked SDK resolves here too.
	bootstrap, err := filepath.EvalSymlinks(filepath.Join(sdkDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("%s bootstrap: %w", SDKPackage, err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("a TypeScript pipeline needs Node.js 22.18 or later on PATH: %w", err)
	}
	if node, err = filepath.Abs(node); err != nil {
		return nil, err
	}
	return Node(node, bootstrap, entry), nil
}

// InstallTypeScript runs npm ci in sparkwingDir with env, less the engine's
// own variables, writing npm's output to stderr.
func InstallTypeScript(ctx context.Context, sparkwingDir string, env []string) error {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("install the TypeScript pipeline's dependencies: %w", err)
	}
	// #nosec G204 -- npm from PATH with fixed arguments.
	cmd := exec.CommandContext(ctx, npm, "ci", "--no-audit", "--no-fund")
	cmd.Dir, cmd.Env = sparkwingDir, engineenv.Strip(env)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm ci in %s: %w", sparkwingDir, err)
	}
	return nil
}

// Prepare returns the command for sparkwingDir's TypeScript pipeline,
// installing its dependencies first when node_modules lacks the SDK.
func Prepare(ctx context.Context, sparkwingDir string, env []string) (Command, error) {
	cmd, err := TypeScript(sparkwingDir)
	if !errors.Is(err, ErrNotInstalled) {
		return cmd, err
	}
	if err := InstallTypeScript(ctx, sparkwingDir, env); err != nil {
		return nil, err
	}
	return TypeScript(sparkwingDir)
}
