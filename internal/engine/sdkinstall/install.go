// Package sdkinstall builds SDK entries inside their selected engine.
package sdkinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Run serves __sdk-install in the selected engine. Version is its build stamp.
// Input contains SDKInstallRequest without Environment. Native tools receive the
// filtered inherited environment, and only the resulting SDKCommand reaches output.
func Run(ctx context.Context, version string, input io.Reader, output, diagnostics io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	var request engine.SDKInstallRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
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
	if selected != version {
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "SDK pin does not match the installing engine"}
	}
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	var command []string
	if request.SDK == engine.GoSDK {
		command, err = installGoSDK(ctx, dir, pin, environment, diagnostics, masker)
	} else {
		command, err = installTypeScriptSDK(ctx, dir, pin, environment, diagnostics, masker)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(engine.SDKCommand{Directory: dir, Command: command})
}

func installGoSDK(ctx context.Context, dir string, pin engine.Pin, environment []string, diagnostics io.Writer, masker *secrets.Masker) (command []string, result error) {
	// safety: workspace and modfile overrides must not replace the SDK that selected this engine.
	environment = append(environment, "GOWORK=off", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		return nil, err
	}
	material, err := json.Marshal([]any{"sdk-install-v2", pin, key, environment})
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(material))
	entry, err := bincache.PipelineEntry(hash[:8] + "-" + hash[8:16])
	if err != nil {
		return nil, err
	}
	lease, _, err := entry.AcquireOrMaterialize(ctx, func(destination string) error {
		var module struct {
			Version string
			Replace any
		}
		body, err := installTool(ctx, dir, environment, "go", "list", "-modfile=go.mod", "-m", "-json", engine.GoModule)
		if err != nil {
			return installDiagnostic(diagnostics, masker, body, err)
		}
		if err := json.Unmarshal(body, &module); err != nil || module.Version != pin.Version || module.Replace != nil {
			return &apierr.Error{Code: apierr.IntegrityFailure, Message: "Go dependency selection changed the SDK pin"}
		}
		body, err = installTool(ctx, dir, environment, "go", "build", "-modfile=go.mod", "-trimpath", "-ldflags", "-s -w", "-o", destination, ".")
		return installDiagnostic(diagnostics, masker, body, err)
	})
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, lease.Release()) }()
	return []string{lease.Path()}, nil
}

func installTypeScriptSDK(ctx context.Context, dir string, pin engine.Pin, environment []string, diagnostics io.Writer, masker *secrets.Masker) ([]string, error) {
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

func installTool(ctx context.Context, dir string, environment []string, name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Dir, command.Env = dir, environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := gotoolchain.Run(ctx, command); err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), fmt.Errorf("SDK dependency tool %s: %w", name, err)
	}
	if name == "go" && len(args) > 0 && args[0] == "list" {
		return stdout.Bytes(), nil
	}
	return append(stdout.Bytes(), stderr.Bytes()...), nil
}

func installDiagnostic(output io.Writer, masker *secrets.Masker, body []byte, toolErr error) error {
	_, err := io.WriteString(output, masker.Mask(string(body)))
	return errors.Join(toolErr, err)
}
