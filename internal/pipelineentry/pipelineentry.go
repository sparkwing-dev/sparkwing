// Package pipelineentry says how a host starts a repository's pipeline as an
// SDK process: a compiled Go binary or Node running a TypeScript entry.
// A .sparkwing whose package.json depends on
// @sparkwing/sdk is TypeScript; any other is a Go module.
package pipelineentry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// protocolArg is the one argument a host appends to an entry's command.
const protocolArg = "--sw-node-protocol"

// sdkPackage is the npm package a TypeScript pipeline depends on.
const sdkPackage = "@sparkwing/sdk"

// errNotInstalled reports a TypeScript pipeline whose dependencies are not
// installed in .sparkwing/node_modules.
var errNotInstalled = errors.New("the TypeScript pipeline's dependencies are not installed")

// Command is the program and the arguments a host runs, with [protocolArg]
// appended, to start one SDK process.
type Command []string

// Binary is the command for a compiled pipeline binary.
func Binary(path string) Command { return Command{path} }

// nodeCommand is the command for a TypeScript entry.
func nodeCommand(node, entry string) Command { return Command{node, entry} }

// Exec returns the command that starts the SDK process.
func (c Command) Exec(environment procenv.Env) *exec.Cmd {
	// #nosec G204 -- the pipeline entry the host resolved, run as argv without a shell.
	return procenv.Command(environment, c[0], append(slices.Clone(c[1:]), protocolArg)...)
}

// safety: each reader decodes only the fields it needs, so a valid manifest
// shaped another way, such as a string exports, never fails a Go pipeline.
func readPackage(path string, into any) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- a package.json under the pipeline directory.
	if err != nil {
		return err
	}
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// IsTypeScript reports whether sparkwingDir holds a TypeScript pipeline: a
// package.json that depends on [sdkPackage].
func IsTypeScript(sparkwingDir string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(sparkwingDir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	typescript, err := isTypeScriptManifest(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", filepath.Join(sparkwingDir, "package.json"), err)
	}
	return typescript, nil
}

// isTypeScriptManifest classifies package.json bytes without reading the working tree.
func isTypeScriptManifest(raw []byte) (bool, error) {
	var pkg struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &pkg); err != nil {
		return false, err
	}
	_, dep := pkg.Dependencies[sdkPackage]
	_, dev := pkg.DevDependencies[sdkPackage]
	return dep || dev, nil
}

// IsPipelineDir reports whether dir is a .sparkwing that holds a pipeline:
// a Go main package or a TypeScript pipeline.
func IsPipelineDir(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err == nil {
		return true, nil
	}
	return IsTypeScript(dir)
}

// TypeScript returns the command for the TypeScript pipeline in sparkwingDir.
// It wraps [errNotInstalled] when node_modules lacks the SDK.
func TypeScript(sparkwingDir string) (Command, error) {
	var pkg struct {
		Main string `json:"main"`
	}
	if err := readPackage(filepath.Join(sparkwingDir, "package.json"), &pkg); err != nil {
		return nil, err
	}
	if pkg.Main == "" {
		return nil, fmt.Errorf("%s names no \"main\" pipeline entry", filepath.Join(sparkwingDir, "package.json"))
	}
	entry := filepath.Join(sparkwingDir, filepath.FromSlash(pkg.Main))
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("pipeline entry: %w", err)
	}
	sdkDir := filepath.Join(sparkwingDir, "node_modules", filepath.FromSlash(sdkPackage))
	var sdk struct{}
	err := readPackage(filepath.Join(sdkDir, "package.json"), &sdk)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has no %s", errNotInstalled, sparkwingDir, sdkPackage)
	}
	if err != nil {
		return nil, err
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("a TypeScript pipeline needs Node.js 22.18 or later on PATH: %w", err)
	}
	if node, err = filepath.Abs(node); err != nil {
		return nil, err
	}
	return nodeCommand(node, entry), nil
}

// installTypeScript runs npm ci with filtered captured inputs and masked output.
func installTypeScript(ctx context.Context, sparkwingDir string, env []string, out io.Writer) error {
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: env, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	cmd, err := NPMCommand(ctx, environment, runtime.GOOS, exec.LookPath, "ci", "--no-audit", "--no-fund")
	if err != nil {
		return fmt.Errorf("install the TypeScript pipeline's dependencies: %w", err)
	}
	cmd.Dir = sparkwingDir
	masked := &installOutput{out: out, stream: masker.Stream()}
	cmd.Stdout, cmd.Stderr = masked, masked
	if err := errors.Join(cmd.Run(), masked.flush()); err != nil {
		return fmt.Errorf("npm ci in %s: %w", sparkwingDir, err)
	}
	return nil
}

type installOutput struct {
	out    io.Writer
	stream *secrets.Stream
}

func (w *installOutput) Write(body []byte) (int, error) {
	text := w.stream.Mask(string(body))
	n, err := io.WriteString(w.out, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(body), nil
}

func (w *installOutput) flush() error {
	_, err := io.WriteString(w.out, w.stream.Flush())
	return err
}

// Prepare returns the command for sparkwingDir's TypeScript pipeline,
// installing its dependencies first when node_modules lacks the SDK, with
// npm's output going to out.
func Prepare(ctx context.Context, sparkwingDir string, env []string, out io.Writer) (Command, error) {
	cmd, err := TypeScript(sparkwingDir)
	if !errors.Is(err, errNotInstalled) {
		return cmd, err
	}
	if err := installTypeScript(ctx, sparkwingDir, env, out); err != nil {
		return nil, err
	}
	return TypeScript(sparkwingDir)
}
