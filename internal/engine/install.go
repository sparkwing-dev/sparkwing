package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	strictjson "sigs.k8s.io/json"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const (
	sdkInstallAttempts = 1
	sdkInstallTimeout  = 10 * time.Minute
)

// SDKInstallRequest gives the verified matching engine its acquired source and filtered install base.
type SDKInstallRequest struct {
	Directory           string          `json:"directory"`
	PipelineDir         string          `json:"pipeline_dir"`
	ExecutableDirectory string          `json:"executable_directory,omitempty"`
	SDK                 SDKKind         `json:"sdk"`
	Environment         []string        `json:"-"`
	Masker              *secrets.Masker `json:"-"`
}

// UnmarshalJSON refuses ambiguous requests and credentials sent through the input pipe.
func (r *SDKInstallRequest) UnmarshalJSON(body []byte) error {
	type request SDKInstallRequest
	var decoded request
	failures, err := strictjson.UnmarshalStrict(body, &decoded)
	if err := errors.Join(err, errors.Join(failures...)); err != nil {
		return err
	}
	*r = SDKInstallRequest(decoded)
	return nil
}

// SDKCommand names the locally built Go command or Node/main command without protocol argv.
type SDKCommand struct {
	Directory        string   `json:"directory"`
	Command          []string `json:"command"`
	CachedExecutable string   `json:"cached_executable,omitempty"`
	ownedDirectory   string
}

// Close removes the installation-owned executable after its consumers finish.
func (c SDKCommand) Close() error {
	if c.ownedDirectory == "" {
		return nil
	}
	return os.RemoveAll(c.ownedDirectory)
}

// InstallSDK installs the SDK using its verified matching engine and filtered environment.
func (r Resolved) InstallSDK(ctx context.Context, request SDKInstallRequest) (_ SDKCommand, resultErr error) {
	if err := ctx.Err(); err != nil {
		return SDKCommand{}, err
	}
	dir, err := request.PipelineDirectory()
	if err != nil {
		return SDKCommand{}, err
	}
	pin, err := ReadPin(dir, request.SDK)
	if err != nil {
		return SDKCommand{}, err
	}
	if pin != r.Identity.Pin {
		return SDKCommand{}, integrity(errors.New("checkout SDK pin changed after engine selection"))
	}
	if err := r.verifyInstalledEngine(); err != nil {
		return SDKCommand{}, err
	}
	masker := request.Masker
	if masker == nil {
		masker = secrets.NewMasker()
	}
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: request.Environment, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return SDKCommand{}, err
	}
	ownedDirectory := ""
	if request.SDK == GoSDK {
		ownedDirectory, err = os.MkdirTemp("", "sparkwing-sdk-executable-")
		if err != nil {
			return SDKCommand{}, err
		}
		request.ExecutableDirectory = ownedDirectory
		defer func() {
			if resultErr != nil {
				resultErr = errors.Join(resultErr, os.RemoveAll(ownedDirectory))
			}
		}()
	}
	body, err := json.Marshal(request)
	if err != nil {
		return SDKCommand{}, err
	}
	// safety: verification selects an absolute executable; PATH cannot substitute the ambient CLI.
	command := exec.Command(r.Executable, "__sdk-install")
	command.Dir, command.Env, command.Stdin = dir, environment, bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = runSDKInstaller(ctx, command, sdkInstallTimeout)
	diagnostic := masker.Mask(stderr.String())
	if err != nil {
		return SDKCommand{}, errors.Join(ctx.Err(), fmt.Errorf("selected engine SDK installation: %w: %s", err, strings.TrimSpace(diagnostic)))
	}
	if _, err := fmt.Fprint(os.Stderr, diagnostic); err != nil {
		return SDKCommand{}, err
	}
	var result SDKCommand
	if err := decodeUniqueJSON(stdout.Bytes(), &result); err != nil {
		return SDKCommand{}, errors.New("selected engine returned an invalid SDK command")
	}
	if !filepath.IsAbs(result.Directory) || len(result.Command) == 0 || !filepath.IsAbs(result.Command[0]) {
		return SDKCommand{}, errors.New("selected engine returned an incomplete SDK command")
	}
	for _, arg := range result.Command {
		if strings.ContainsRune(arg, 0) || arg == "--sw-node-protocol" || arg == "--import" || strings.HasPrefix(arg, "--import=") || arg == "--require" || strings.HasPrefix(arg, "--require=") {
			return SDKCommand{}, errors.New("selected engine returned protocol arguments or a preload in its SDK command")
		}
	}
	if request.SDK == GoSDK {
		name := "pipeline"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if len(result.Command) != 1 || result.Command[0] != filepath.Join(ownedDirectory, name) {
			return SDKCommand{}, errors.New("selected engine returned an unowned Go executable")
		}
		info, err := os.Lstat(result.Command[0])
		if err != nil {
			return SDKCommand{}, err
		}
		if !info.Mode().IsRegular() {
			return SDKCommand{}, errors.New("selected engine returned a nonregular Go executable")
		}
	}
	result.ownedDirectory = ownedDirectory
	return result, nil
}

func runSDKInstaller(ctx context.Context, command *exec.Cmd, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runSDKInstallProcess(ctx, command); err != nil {
		return fmt.Errorf("SDK install failed after %d attempt (deadline %s): %w", sdkInstallAttempts, timeout, errors.Join(ctx.Err(), err))
	}
	return nil
}

// PipelineDirectory resolves the acquired pipeline without escaping its checkout.
func (request SDKInstallRequest) PipelineDirectory() (string, error) {
	if !filepath.IsAbs(request.Directory) || (request.PipelineDir != "" && !filepath.IsLocal(request.PipelineDir)) {
		return "", errors.New("SDK installation requires an absolute checkout and a relative pipeline directory")
	}
	root, err := filepath.EvalSymlinks(request.Directory)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, request.PipelineDir))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("SDK pipeline directory escapes the acquired checkout")
	}
	return dir, nil
}

func (r Resolved) verifyInstalledEngine() error {
	if r.Identity.Pin.SDK == GoSDK && r.Identity.Pin.Replacement != "" {
		current, err := installedEngine(context.Background(), r.Identity.Pin, r.Target)
		if err == nil && (current.Identity != r.Identity || current.AssetDigest != r.AssetDigest || current.Executable != r.Executable) {
			err = integrity(errors.New("SDK installation requires the installed replacement-pin engine"))
		}
		return err
	}
	version, err := VersionForPin(r.Identity.Pin)
	if err != nil {
		return err
	}
	if version != r.Identity.Version || !validDigest(r.Identity.ManifestDigest) || !validDigest(r.AssetDigest) || !filepath.IsAbs(r.Executable) || r.Target != (releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
		return integrity(errors.New("SDK installation requires this machine's exact verified Sparkwing engine"))
	}
	release, err := readCachedRelease(version, r.Target, r.Executable)
	if err != nil {
		return integrity(err)
	}
	if digest(release.bundle.Manifest) != r.Identity.ManifestDigest || release.Asset().Digest() != r.AssetDigest {
		return integrity(errors.New("installed engine differs from the selected release"))
	}
	return nil
}
