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

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (r Resolved) installSDK(ctx context.Context, request SDKInstallRequest) (SDKCommand, error) {
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
	masker := secrets.NewMasker()
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: request.Environment, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return SDKCommand{}, err
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
	err = gotoolchain.Run(ctx, command)
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
	return result, nil
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
	version, err := VersionForPin(r.Identity.Pin)
	if err != nil {
		return err
	}
	if version != r.Identity.Version || !validDigest(r.Identity.ManifestDigest) || !validDigest(r.AssetDigest) || !filepath.IsAbs(r.Executable) || r.Target != (releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
		return integrity(errors.New("SDK installation requires this machine's exact verified Sparkwing engine"))
	}
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(r.Executable), "verification.json"))
	if err != nil {
		return integrity(err)
	}
	var bundle ReleaseBundle
	if err := decodeUniqueJSON(metadata, &bundle); err != nil {
		return integrity(errors.New("invalid selected engine verification record"))
	}
	bundle.Asset, err = os.ReadFile(r.Executable)
	if err != nil {
		return integrity(err)
	}
	release, err := VerifyRelease(version, r.Target, bundle)
	if err != nil {
		return err
	}
	if digest(bundle.Manifest) != r.Identity.ManifestDigest || release.Asset().Digest() != r.AssetDigest {
		return integrity(errors.New("installed engine differs from the selected release"))
	}
	return nil
}
