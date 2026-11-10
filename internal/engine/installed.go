package engine

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
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
	path, err := os.Executable()
	if err != nil {
		return Resolved{}, err
	}
	return replacementEngineAt(ctx, pin, target, path, false)
}

func replacementEngineAt(ctx context.Context, pin engineid.Pin, target releaseasset.Target, path string, retained bool) (Resolved, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	if (pin.SDK != engineid.GoSDK && pin.SDK != engineid.TypeScriptSDK) || pin.Replacement == "" || target != (releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
		return Resolved{}, invalid("SDK replacement requires this machine's installed Sparkwing engine", "Run the pipeline on the installed CLI's platform.")
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return Resolved{}, err
	}
	if retained {
		settings := make(map[string]string, len(info.Settings))
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		if settings["GOOS"] != target.GOOS || settings["GOARCH"] != target.GOARCH || (info.Path != HostModule+"/cmd/"+string(target.Binary) && info.Path != HostModule+"/cmd/"+string(releaseasset.SparkwingRunner)) {
			return Resolved{}, integrity(errors.New("retained executable does not match the native Sparkwing target"))
		}
		stat, err := os.Stat(path)
		if err != nil {
			return Resolved{}, err
		}
		if !stat.Mode().IsRegular() || (runtime.GOOS != "windows" && stat.Mode().Perm()&0o100 == 0) {
			return Resolved{}, integrity(errors.New("retained engine is not executable"))
		}
	}
	version, err := executableVersion(info)
	if err != nil {
		return Resolved{}, err
	}
	if version == "" {
		version = "(devel)"
	}
	digestEngine := executableDigest
	if retained {
		digestEngine = fullExecutableDigest
	}
	digest, err := digestEngine(ctx, path)
	if err != nil {
		return Resolved{}, err
	}
	resolved := Resolved{
		Identity: engineid.Identity{Pin: pin, Version: version, ManifestDigest: digest}, Target: target, AssetDigest: digest, Executable: path,
		Notices: []engineid.Notice{PinDisabledNotice(version)},
	}
	if retained {
		resolved.retainedExecutable = path
		resolved.retainedIdentity = resolved.Identity
		resolved.Notices[0].Reason = "SDK replacement: engine pinning is off for this run; using the accepted local engine."
	}
	return resolved, nil
}

// PinDisabledNotice reports that an SDK replacement runs the installed engine without release pinning or version policy.
func PinDisabledNotice(version string) engineid.Notice {
	return engineid.Notice{Kind: engineid.NoticePinDisabled, Version: version, Reason: "SDK replacement: engine pinning is off for this run; using the installed CLI's engine."}
}

var selfDigest struct {
	sync.Mutex
	info   os.FileInfo
	digest string
}

// perf: a local run resolves and verifies its replacement engine about eight times, and each hash of the 100 MB
// executable took 250 ms; the digest is kept while the file keeps its identity, size and modification time.
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
	identity, stable := fileIdentity(info, time.Now())
	record := ""
	// hack: go test binaries resolve themselves as the engine; they must not leave records in a real home.
	if !strings.HasSuffix(filepath.Base(path), ".test") {
		record = selfDigestRecord(path)
	}
	if stable && record != "" {
		if body, err := os.ReadFile(record); err == nil {
			if recorded, digest, ok := strings.Cut(strings.TrimSpace(string(body)), " "); ok && recorded == path+"|"+identity && validDigest(digest) {
				selfDigest.info, selfDigest.digest = info, digest
				return digest, nil
			}
		}
	}
	value, err := fullExecutableDigest(ctx, path)
	if err != nil {
		return "", err
	}
	selfDigest.info, selfDigest.digest = info, value
	if after, err := os.Stat(path); stable && record != "" && err == nil {
		if again, ok := fileIdentity(after, time.Now()); ok && again == identity {
			writeSelfDigestRecord(record, path+"|"+identity+" "+selfDigest.digest)
		}
	}
	return selfDigest.digest, nil
}

func fullExecutableDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	if err := errors.Join(readErr, file.Close(), ctx.Err()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// perf: each sparkwing process hashed its own 100 MB executable (250 ms) before running a replacement-pin
// pipeline; each executable path keeps its exact file identity so installed and retained copies do not evict each other.
func selfDigestRecord(path string) string {
	p, err := paths.DefaultPaths()
	if err != nil || p.Root == "" {
		return ""
	}
	return filepath.Join(p.Root, "cache", "engine-self-digests", digest([]byte(path)))
}

// safety: a lost or torn record only costs one hash; the read requires an exact identity and a valid digest.
func writeSelfDigestRecord(record, line string) {
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		return
	}
	stage, err := os.CreateTemp(filepath.Dir(record), ".engine-self-digest-")
	if err != nil {
		return
	}
	_, writeErr := stage.WriteString(line + "\n")
	if errors.Join(writeErr, stage.Close()) != nil || os.Rename(stage.Name(), record) != nil {
		if err := os.Remove(stage.Name()); err != nil && !os.IsNotExist(err) {
			slog.Default().Warn("engine: self-digest stage outlived its write", "path", stage.Name(), "error", err)
		}
	}
}

func (r *Resolver) replacementEngine(ctx context.Context, pin engineid.Pin, target releaseasset.Target) (Resolved, error) {
	installed := r.installed
	if installed == nil {
		installed = os.Executable
	}
	path, err := installed()
	if err != nil {
		return Resolved{}, err
	}
	resolved, err := replacementEngineAt(ctx, pin, target, path, false)
	if err != nil || r.config.LocalBuildDirectory == "" {
		return resolved, err
	}
	path, _, err = RetainExecutable(r.config.LocalBuildDirectory, path, resolved.AssetDigest)
	if err != nil {
		return Resolved{}, err
	}
	return replacementEngineAt(ctx, pin, target, path, true)
}
