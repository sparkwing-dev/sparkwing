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

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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

func installGoSDK(ctx context.Context, dir string, request engine.SDKInstallRequest, pin engineid.Pin, environment procenv.Env, diagnostics io.Writer, masker *secrets.Masker) (command []string, cachedExecutable string, result error) {
	if !filepath.IsAbs(request.ExecutableDirectory) {
		return nil, "", errors.New("Go installation requires an owned executable directory")
	}
	flags := ""
	for setting := range environment.Values() {
		if value, ok := strings.CutPrefix(setting, "GOFLAGS="); ok {
			flags = value
		}
	}
	environment, err := environment.GoTool("GOWORK=off", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "GOFLAGS="+flags+" -modfile=go.mod")
	if err != nil {
		return nil, "", err
	}
	qa, err := engine.QAModuleEnvironment(pin, func(args ...string) ([]byte, error) {
		return installTool(ctx, dir, environment, "go", args...)
	})
	if err != nil {
		return nil, "", err
	}
	environment, err = environment.GoTool(qa...)
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
	args := []string{"list"}
	if overlay := bincache.EffectiveOverlay(dir, slices.Collect(environment.Values())); overlay != "" {
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, "-m", "-json", engine.GoModule)
	body, err := installTool(ctx, dir, environment, "go", args...)
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

func installTypeScriptSDK(ctx context.Context, dir string, pin engineid.Pin, environment procenv.Env, diagnostics io.Writer, masker *secrets.Masker) ([]string, error) {
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
	body, err = installTool(ctx, dir, environment, "npm", "ci", "--no-audit", "--no-fund")
	if err := installDiagnostic(diagnostics, masker, body, err); err != nil {
		return nil, err
	}
	body, err = os.ReadFile(filepath.Join(dir, "node_modules", "@sparkwing", "sdk", "package.json"))
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
