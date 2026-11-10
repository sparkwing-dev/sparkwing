package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: local runs, the local controller, update and upgrade must read and refresh one policy cache.
func defaultCacheDirectory() (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Root, "engines"), nil
}

// NewResolver initializes private machine storage and host-owned transport.
func NewResolver(config Config) (*Resolver, error) {
	var err error
	if config.TrustedKeys == nil {
		config.TrustedKeys, err = releaseauth.PublicKeys()
		if err != nil {
			return nil, err
		}
	}
	keys := make([]ed25519.PublicKey, len(config.TrustedKeys))
	for i, key := range config.TrustedKeys {
		keys[i] = bytes.Clone(key)
	}
	config.TrustedKeys = keys
	config.Mode, err = toolchain.ParseMode(string(config.Mode))
	if err != nil {
		return nil, err
	}
	if config.CacheDirectory == "" {
		config.CacheDirectory, err = defaultCacheDirectory()
		if err != nil {
			return nil, err
		}
	}
	config.CacheDirectory, err = filepath.Abs(config.CacheDirectory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.CacheDirectory, 0o700); err != nil {
		return nil, err
	}
	if config.LocalBuildDirectory != "" {
		if !filepath.IsAbs(config.LocalBuildDirectory) {
			return nil, errors.New("retained local engine directory must be absolute")
		}
		config.LocalBuildDirectory = filepath.Clean(config.LocalBuildDirectory)
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &Resolver{config: config, installed: os.Executable}, nil
}

// Resolve obtains the exact pin's permitted executable, including on a warm cache.
func (r *Resolver) Resolve(ctx context.Context, pin engineid.Pin, target releaseasset.Target) (Resolved, error) {
	if pin.Replacement != "" {
		return r.replacementEngine(ctx, pin, target)
	}
	version, err := r.EngineVersion(ctx, pin)
	if err != nil {
		return Resolved{}, err
	}
	return r.resolve(ctx, engineid.Identity{Pin: pin, Version: version}, target)
}

// EngineVersion resolves a pin through signed SDK and engine policy without downloading executables.
func (r *Resolver) EngineVersion(ctx context.Context, pin engineid.Pin) (string, error) {
	version, err := SDKVersionForPin(pin)
	if err != nil {
		return "", err
	}
	if pin.Replacement != "" {
		return installedEngineVersion()
	}
	policy, err := r.sdkIndex(ctx, time.Now())
	if err != nil {
		return "", err
	}
	sdk, _, err := SelectSDK(policy.Index, pin.SDK, version, time.Now())
	if err != nil {
		return "", err
	}
	return SelectEngine(policy.Index, sdk, time.Now())
}

// Ensure binds another machine's executable to the accepted portable identity.
// A configured local build directory can retain the exact accepted Go replacement
// engine after the installed CLI changes; new resolutions still use the installed CLI.
func (r *Resolver) Ensure(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, error) {
	if identity.Pin.Replacement != "" {
		resolved, err := r.replacementEngine(ctx, identity.Pin, target)
		if err == nil && resolved.Identity == identity {
			return resolved, nil
		}
		if r.config.LocalBuildDirectory == "" {
			if err != nil {
				return Resolved{}, err
			}
			return Resolved{}, &apierr.Error{
				Code: hosterr.IntegrityFailure,
				Message: fmt.Sprintf("planned on engine %s (sha256 %s), this host has %s (sha256 %s); route planning to the executing host (repository runner requirements) or install %s (sha256 %s)",
					identity.Version, identity.ManifestDigest, resolved.Identity.Version, resolved.Identity.ManifestDigest, identity.Version, identity.ManifestDigest),
			}
		}
		if !validDigest(identity.ManifestDigest) {
			return Resolved{}, integrity(errors.New("accepted local engine digest is invalid"))
		}
		name := identity.ManifestDigest
		if target.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(r.config.LocalBuildDirectory, name)
		resolved, err = replacementEngineAt(ctx, identity.Pin, target, path, true)
		if err != nil {
			return Resolved{}, integrity(fmt.Errorf("read accepted retained local engine: %w", err))
		}
		if resolved.Identity != identity {
			return Resolved{}, integrity(errors.New("retained local engine differs from the accepted replacement-pin engine"))
		}
		return resolved, nil
	}
	if !releaseVersion(identity.Version) || !validDigest(identity.ManifestDigest) {
		return Resolved{}, integrity(fmt.Errorf("accepted engine identity is invalid"))
	}
	return r.resolve(ctx, identity, target)
}

func (r *Resolver) resolve(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, error) {
	if target.Binary != releaseasset.Sparkwing {
		return Resolved{}, invalid("pipeline engines require the sparkwing executable", "Select the pipeline engine target.")
	}
	if _, err := target.NameForVersion(identity.Version); err != nil {
		return Resolved{}, err
	}
	now := time.Now()
	policy, err := r.sdkIndex(ctx, now)
	if err != nil {
		return Resolved{}, err
	}
	notices, err := Evaluate(policy.Index, identity.Version, now)
	if err != nil {
		return Resolved{}, err
	}
	sdkNotices, err := acceptsSDK(policy.Index, identity.Pin, identity.Version, now)
	if err != nil {
		return Resolved{}, err
	}
	notices = append(append(policy.Notices, notices...), sdkNotices...)
	cached, ok, err := r.findCached(ctx, identity, target)
	if err != nil {
		return Resolved{}, err
	}
	if ok {
		cached.Notices = notices
		return cached, nil
	}
	if r.config.Mode == toolchain.ModeLocal {
		return Resolved{}, &apierr.Error{Code: hosterr.Unavailable, Message: "Exact verified engine is absent from the machine cache", Hint: "Use auto toolchain mode once to fetch the matching official release."}
	}
	release, err := r.fetchRelease(ctx, identity.Version, target)
	if err != nil {
		return Resolved{}, err
	}
	if identity.ManifestDigest != "" && digest(release.bundle.Manifest) != identity.ManifestDigest {
		return Resolved{}, integrity(fmt.Errorf("release manifest differs from accepted engine identity"))
	}
	resolved, err := r.storeVerified(ctx, identity.Pin, release)
	resolved.Notices = notices
	return resolved, err
}

func (r *Resolver) findCached(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, bool, error) {
	if identity.ManifestDigest != "" {
		return r.cached(ctx, identity, target)
	}
	dir, err := r.targetDirectory(identity.Version, target)
	if err != nil {
		return Resolved{}, false, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return Resolved{}, false, nil
	}
	if err != nil {
		return Resolved{}, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validDigest(entry.Name()) {
			continue
		}
		identity.ManifestDigest = entry.Name()
		resolved, ok, err := r.cached(ctx, identity, target)
		if err != nil || ok {
			return resolved, ok, err
		}
	}
	return Resolved{}, false, nil
}

func (r *Resolver) cached(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, bool, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, false, err
	}
	version := identity.Version
	if !releaseVersion(version) || !validDigest(identity.ManifestDigest) {
		return Resolved{}, false, integrity(fmt.Errorf("invalid cache identity"))
	}
	dir, err := r.targetDirectory(version, target)
	if err != nil {
		return Resolved{}, false, err
	}
	dir = filepath.Join(dir, identity.ManifestDigest)
	name, err := target.NameForVersion(version)
	if err != nil {
		return Resolved{}, false, err
	}
	executable := filepath.Join(dir, name)
	info, err := os.Stat(executable)
	if os.IsNotExist(err) {
		return Resolved{}, false, nil
	}
	if err != nil {
		return Resolved{}, false, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0) {
		return Resolved{}, false, cachedIntegrity(executable, errors.New("cached engine is not an executable regular file"))
	}
	verified, err := r.verifyCached(ctx, version, target, executable)
	if err != nil {
		return Resolved{}, false, cachedIntegrity(executable, err)
	}
	if verified.manifestDigest != identity.ManifestDigest {
		return Resolved{}, false, cachedIntegrity(executable, errors.New("cached release manifest differs from the accepted engine"))
	}
	return Resolved{verifiedIdentity: identity, Identity: identity, Target: target, AssetDigest: verified.assetDigest, Executable: executable, verification: verified}, true, nil
}

func readCachedRelease(version string, target releaseasset.Target, executable string, keys []ed25519.PublicKey) (VerifiedRelease, error) {
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "verification.json"))
	if err != nil {
		return VerifiedRelease{}, err
	}
	return verifyCachedRecord(version, target, executable, keys, metadata)
}

func verifyCachedRecord(version string, target releaseasset.Target, executable string, keys []ed25519.PublicKey, metadata []byte) (VerifiedRelease, error) {
	var bundle ReleaseBundle
	if err := decodeUniqueJSON(metadata, &bundle); err != nil {
		return VerifiedRelease{}, integrity(errors.New("invalid cached release verification record"))
	}
	var err error
	bundle.Asset, err = os.ReadFile(executable)
	if err != nil {
		return VerifiedRelease{}, err
	}
	return VerifyRelease(version, target, bundle, keys)
}

// storeVerified publishes a complete verified cache entry by an atomic directory rename.
func (r *Resolver) storeVerified(ctx context.Context, pin engineid.Pin, release VerifiedRelease) (resolved Resolved, result error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	version := release.version
	if !releaseVersion(version) || release.asset.Name() == "" {
		return Resolved{}, integrity(fmt.Errorf("release capability does not match SDK pin"))
	}
	// perf: a VerifiedRelease is VerifyRelease's result, so trusting every key it was checked under stands in
	// for hashing and verifying its 70 MB again, which cost ~0.4s on every first run.
	for _, key := range release.keys {
		if !slices.ContainsFunc(r.config.TrustedKeys, func(trusted ed25519.PublicKey) bool { return trusted.Equal(key) }) {
			return Resolved{}, integrity(fmt.Errorf("release was verified under a key this resolver does not trust"))
		}
	}
	identity := engineid.Identity{Pin: pin, Version: version, ManifestDigest: digest(release.bundle.Manifest)}
	if cached, ok, err := r.cached(ctx, identity, release.target); err != nil || ok {
		return cached, err
	}
	parent, err := r.targetDirectory(version, release.target)
	if err != nil {
		return Resolved{}, err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Resolved{}, err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return Resolved{}, err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(stage)) }()
	bundle := release.bundle
	bundle.Asset = nil
	metadata, err := json.Marshal(bundle)
	if err != nil {
		return Resolved{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, "verification.json"), metadata, 0o600); err != nil {
		return Resolved{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, release.asset.Name()), release.asset.Bytes(), 0o700); err != nil {
		return Resolved{}, err
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	destination := filepath.Join(parent, identity.ManifestDigest)
	if err := os.Rename(stage, destination); err != nil {
		if cached, ok, cacheErr := r.cached(ctx, identity, release.target); cacheErr != nil || ok {
			return cached, cacheErr
		}
		if err := os.RemoveAll(destination); err != nil {
			return Resolved{}, err
		}
		if err := os.Rename(stage, destination); err != nil {
			return Resolved{}, err
		}
	}
	executable := filepath.Join(destination, release.asset.Name())
	verified := &cacheVerification{executable: executable, version: version, target: release.target, manifestDigest: identity.ManifestDigest, assetDigest: release.asset.Digest(), metadataDigest: digest(metadata)}
	if err := verified.check(ctx); err != nil {
		return Resolved{}, cachedIntegrity(executable, err)
	}
	verifiedReleases.Lock()
	rememberVerifiedLocked(r.verificationKey(version, release.target, executable), verified)
	verifiedReleases.Unlock()
	return Resolved{verifiedIdentity: identity, Identity: identity, Target: release.target, AssetDigest: release.asset.Digest(), Executable: executable, verification: verified}, nil
}

func (r *Resolver) targetDirectory(version string, target releaseasset.Target) (string, error) {
	name, err := target.NameForVersion(version)
	if err != nil {
		return "", err
	}
	return filepath.Join(r.config.CacheDirectory, "releases", version, name), nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
