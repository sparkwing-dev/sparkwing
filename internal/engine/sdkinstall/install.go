// Package sdkinstall builds SDK entries inside their selected engine.
package sdkinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// Run serves __sdk-install with the selected engine's build stamp and filtered environment.
func Run(ctx context.Context, version string, input io.Reader, output, diagnostics io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var request engine.SDKInstallRequest
	decoder := json.NewDecoder(input)
	if err := decoder.Decode(&request); err != nil {
		return errors.New("invalid SDK install request")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing SDK install request data")
	}
	dir, err := request.PipelineDirectory()
	if err != nil {
		return err
	}
	pin, err := engine.ReadPin(dir, request.SDK)
	if err != nil {
		return err
	}
	selected, err := engine.VersionForPin(pin)
	if err != nil {
		return err
	}
	if pin.Replacement == "" && selected != version {
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "SDK pin does not match the installing engine"}
	}
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	var command []string
	var cachedExecutable string
	if request.SDK == engineid.GoSDK {
		command, cachedExecutable, err = installGoSDK(ctx, dir, request, pin, environment, diagnostics, masker)
	} else {
		command, err = installTypeScriptSDK(ctx, dir, pin, environment, diagnostics, masker)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(engine.SDKCommand{Directory: dir, Command: command, CachedExecutable: cachedExecutable})
}

// GoEnvironment applies the native SDK compiler settings used by installation and cache diagnostics.
func GoEnvironment(ctx context.Context, dir string, pin engineid.Pin, environment procenv.Env) (procenv.Env, error) {
	flags := ""
	for setting := range environment.Values() {
		if value, ok := strings.CutPrefix(setting, "GOFLAGS="); ok {
			flags = value
		}
	}
	environment, err := environment.GoTool("GOWORK=off", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "GOFLAGS="+flags+" -modfile=go.mod")
	if err != nil {
		return procenv.Env{}, err
	}
	qa, err := engine.QAModuleEnvironment(pin, func(args ...string) ([]byte, error) {
		return installTool(ctx, dir, environment, "go", args...)
	})
	if err != nil {
		return procenv.Env{}, err
	}
	environment, err = environment.GoTool(qa...)
	if err != nil {
		return procenv.Env{}, err
	}
	return environment, nil
}

func installGoSDK(ctx context.Context, dir string, request engine.SDKInstallRequest, pin engineid.Pin, environment procenv.Env, diagnostics io.Writer, masker *secrets.Masker) (command []string, cachedExecutable string, result error) {
	if !filepath.IsAbs(request.ExecutableDirectory) {
		return nil, "", errors.New("SDK installation requires an owned executable directory")
	}
	// safety: generated sparks overlays can predate an SDK upgrade. The native
	// manifest owns the engine pin, while the overlay owns the sparks versions.
	if overlay := bincache.EffectiveOverlay(dir, []string{"GOWORK=off"}); overlay != "" {
		if err := refreshOverlayPin(overlay, pin.Version); err != nil {
			return nil, "", err
		}
	}
	environment, err := GoEnvironment(ctx, dir, pin, environment)
	if err != nil {
		return nil, "", err
	}
	var buildDiagnostics bytes.Buffer
	options := bincache.CompileOptions{Environment: slices.Collect(environment.Values()), Diagnostics: &buildDiagnostics, HashAllFiles: request.HashAllFiles}
	name := "pipeline"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(request.ExecutableDirectory, name)
	selection := startSelection(ctx, dir, environment)
	defer selection.wait()
	var lease *bincache.Lease
	uncached := request.NoBincache
	if !uncached {
		lease, _, err = bincache.MaterializePipeline(ctx, dir, options)
		if errors.Is(err, bincache.ErrUnstableInputs) {
			fmt.Fprintln(&buildDiagnostics, "pipeline build inputs kept changing; building without the binary cache")
			uncached, err = true, nil
		}
	}
	if uncached {
		err = bincache.CompileUncachedPipeline(ctx, dir, path, options)
	} else {
		if lease != nil {
			defer func() { result = errors.Join(result, lease.Release()) }()
		}
	}
	if err := installDiagnostic(diagnostics, masker, buildDiagnostics.Bytes(), err); err != nil {
		return nil, "", err
	}
	body, err := selection.result(ctx, dir, environment)
	if err != nil {
		return nil, "", installDiagnostic(diagnostics, masker, body, err)
	}
	var module struct {
		Version string
		Replace *struct{ Path, Version string }
	}
	if err := json.Unmarshal(body, &module); err != nil {
		return nil, "", err
	}
	replacement := ""
	if module.Replace != nil {
		replacement = module.Replace.Path
		if module.Replace.Version != "" {
			replacement += "@" + module.Replace.Version
		}
	}
	if module.Version != pin.Version || replacement != pin.Replacement {
		return nil, "", &apierr.Error{Code: apierr.IntegrityFailure, Message: "Go dependency selection changed the SDK pin"}
	}
	if uncached {
		return []string{path}, "", nil
	}
	binary, err := lease.Install(path)
	if err != nil {
		return nil, "", err
	}
	return []string{binary.Path()}, lease.CachedPath(), nil
}

// perf: the SDK pin check's `go list -m` (150-300 ms) runs while the pipeline key is computed; the answer is
// used only if the module files and overlay it read are unchanged afterwards, otherwise it runs again.
type pendingSelection struct {
	args   []string
	inputs string
	done   chan struct{}
	body   []byte
	err    error
}

func startSelection(ctx context.Context, dir string, environment procenv.Env) *pendingSelection {
	args, inputs := selectionInputs(dir, environment)
	p := &pendingSelection{args: args, inputs: inputs, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.body, p.err = installTool(ctx, dir, environment, "go", p.args...)
	}()
	return p
}

func (p *pendingSelection) wait() { <-p.done }

func (p *pendingSelection) result(ctx context.Context, dir string, environment procenv.Env) ([]byte, error) {
	p.wait()
	args, inputs := selectionInputs(dir, environment)
	if slices.Equal(args, p.args) && inputs == p.inputs {
		return p.body, p.err
	}
	return installTool(ctx, dir, environment, "go", args...)
}

func selectionInputs(dir string, environment procenv.Env) ([]string, string) {
	args := []string{"list"}
	files := []string{filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum")}
	if overlay := bincache.EffectiveOverlay(dir, slices.Collect(environment.Values())); overlay != "" {
		args = append(args, "-modfile="+overlay)
		files = append(files, overlay, strings.TrimSuffix(overlay, ".mod")+".sum")
	}
	var inputs strings.Builder
	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			fmt.Fprintf(&inputs, "%s:%d:%d;", file, info.Size(), info.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&inputs, "%s:absent;", file)
		}
	}
	return append(args, "-m", "-json", engine.GoModule), inputs.String()
}

func installTypeScriptSDK(ctx context.Context, dir string, pin engineid.Pin, environment procenv.Env, diagnostics io.Writer, masker *secrets.Masker) (_ []string, result error) {
	// bug: npm ci removes packages beneath running SDK processes. Serialize npm's
	// incremental installs so npm owns reuse without replacing an unchanged tree.
	lock, err := os.Open(filepath.Join(dir, pin.Lockfile))
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	if err := lockTypeScriptInstall(lock); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var manifest struct{ Main string }
	body, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}
	if manifest.Main == "" {
		return nil, errors.New("TypeScript package.json requires a main entry")
	}
	// safety: npm install reuses the tree, but only ci checks that every dependency is locked.
	body, err = installTool(ctx, dir, environment, "npm", "ci", "--dry-run", "--ignore-scripts", "--no-audit", "--no-fund")
	if err := installDiagnostic(diagnostics, masker, body, err); err != nil {
		return nil, err
	}
	body, err = installTool(ctx, dir, environment, "npm", "install", "--no-save", "--no-audit", "--no-fund")
	if err := installDiagnostic(diagnostics, masker, body, err); err != nil {
		return nil, err
	}
	installedManifest := filepath.Join(dir, "node_modules", "@sparkwing", "sdk", "package.json")
	body, err = os.ReadFile(installedManifest)
	if err != nil {
		return nil, err
	}
	var installed struct{ Version string }
	if err := json.Unmarshal(body, &installed); err != nil || installed.Version != pin.Version {
		return nil, &apierr.Error{Code: apierr.IntegrityFailure, Message: "installed TypeScript SDK differs from the native lockfile pin"}
	}
	main := filepath.Join(dir, manifest.Main)
	if _, err := os.Stat(main); err != nil {
		return nil, err
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("TypeScript installation requires Node.js on PATH: %w", err)
	}
	node, err = filepath.Abs(node)
	return []string{node, main}, err
}

func installTool(ctx context.Context, dir string, environment procenv.Env, name string, args ...string) ([]byte, error) {
	command := procenv.Command(environment, name, args...)
	if name == "npm" {
		var err error
		command, err = pipelineentry.NPMCommand(ctx, environment, runtime.GOOS, exec.LookPath, args...)
		if err != nil {
			return nil, fmt.Errorf("resolve npm: %w", err)
		}
	}
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := gotoolchain.Run(ctx, command); err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), fmt.Errorf("SDK dependency tool %s: %w", name, err)
	}
	if name == "go" && len(args) > 0 && (args[0] == "list" || args[0] == "env") {
		return stdout.Bytes(), nil
	}
	return append(stdout.Bytes(), stderr.Bytes()...), nil
}

func installDiagnostic(output io.Writer, masker *secrets.Masker, body []byte, toolErr error) error {
	_, err := io.WriteString(output, masker.Mask(string(body)))
	return errors.Join(toolErr, err)
}

func refreshOverlayPin(overlay, version string) (result error) {
	// safety: the overlay sits in repository content, so a committed symlink must not redirect this
	// rewrite to a file outside the pipeline directory on the runner.
	root, err := os.OpenRoot(filepath.Dir(overlay))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	name := filepath.Base(overlay)
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SDK installation refuses overlay %s: it is not a regular file", overlay)
	}
	body, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	file, err := modfile.Parse(overlay, body, nil)
	if err != nil {
		return err
	}
	if err := file.AddRequire(engine.GoModule, version); err != nil {
		return err
	}
	updated, err := file.Format()
	if err != nil {
		return err
	}
	if bytes.Equal(body, updated) {
		return nil
	}
	return root.WriteFile(name, updated, 0o600)
}
