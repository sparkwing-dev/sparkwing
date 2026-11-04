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
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
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

func installedEngine(ctx context.Context, pin engineid.Pin, target releaseasset.Target) (Resolved, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	if pin.SDK != engineid.GoSDK || pin.Replacement == "" || target != (releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
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
	digest, err := executableDigest(ctx, path)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Identity: engineid.Identity{Pin: pin, Version: version, ManifestDigest: digest}, Target: target, AssetDigest: digest, Executable: path,
		Notices: []engineid.Notice{{Kind: engineid.NoticePinDisabled, Version: version, Reason: "SDK replacement: engine pinning is off for this run; using the installed CLI's engine."}},
	}, nil
}

var selfDigest struct {
	sync.Mutex
	info   os.FileInfo
	digest string
}

// executableDigest hashes the running executable once per process while the file keeps its identity, size and
// modification time.
// perf: one local run resolves and verifies its replacement engine about eight times; each hash of a 100 MB
// executable took 250 ms.
func executableDigest(ctx context.Context, path string) (string, error) {
	selfDigest.Lock()
	defer selfDigest.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if previous := selfDigest.info; previous != nil && os.SameFile(previous, info) && previous.Size() == info.Size() && previous.ModTime().Equal(info.ModTime()) {
		return selfDigest.digest, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	if err := errors.Join(readErr, file.Close(), ctx.Err()); err != nil {
		return "", err
	}
	selfDigest.info, selfDigest.digest = info, hex.EncodeToString(hash.Sum(nil))
	return selfDigest.digest, nil
}
