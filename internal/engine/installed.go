package engine

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func installedEngineVersion() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	version, err := executableVersion(info)
	if err != nil || version != "" {
		return version, err
	}
	return "(devel)", nil
}

func installedEngine(ctx context.Context, pin Pin, target releaseasset.Target) (Resolved, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	if pin.SDK != GoSDK || pin.Replacement == "" || target != (releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
		return Resolved{}, invalid("SDK replacement requires this machine's installed Sparkwing engine", "Run the pipeline on the installed CLI's platform.")
	}
	version, err := installedEngineVersion()
	if err != nil {
		return Resolved{}, err
	}
	path, err := os.Executable()
	if err != nil {
		return Resolved{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Resolved{}, err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	if err := errors.Join(readErr, file.Close(), ctx.Err()); err != nil {
		return Resolved{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	return Resolved{
		Identity: Identity{Pin: pin, Version: version, ManifestDigest: digest}, Target: target, AssetDigest: digest, Executable: path,
		Notices: []Notice{{Kind: NoticePinDisabled, Version: version, Reason: "SDK replacement: engine pinning is off for this run; using the installed CLI's engine."}},
	}, nil
}
