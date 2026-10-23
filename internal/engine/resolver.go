package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// DefaultCacheDirectory is shared by execution placements on this machine.
func DefaultCacheDirectory() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sparkwing", "engines"), nil
}

// NewResolver initializes private machine storage and host-owned transport.
func NewResolver(config Config) (*Resolver, error) {
	var err error
	config.Mode, err = toolchain.ParseMode(string(config.Mode))
	if err != nil {
		return nil, err
	}
	if config.CacheDirectory == "" {
		config.CacheDirectory, err = DefaultCacheDirectory()
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
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &Resolver{config: config}, nil
}

// Resolve obtains the exact pin's permitted executable, including on a warm cache.
func (r *Resolver) Resolve(ctx context.Context, pin engineid.Pin, target releaseasset.Target) (Resolved, error) {
	if pin.SDK == engineid.GoSDK && pin.Replacement != "" {
		return installedEngine(ctx, pin, target)
	}
	version, err := VersionForPin(pin)
	if err != nil {
		return Resolved{}, err
	}
	return r.resolve(ctx, engineid.Identity{Pin: pin, Version: version}, target)
}

// Ensure binds another machine's executable to the accepted portable identity.
func (r *Resolver) Ensure(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, error) {
	if identity.Pin.SDK == engineid.GoSDK && identity.Pin.Replacement != "" {
		resolved, err := installedEngine(ctx, identity.Pin, target)
		if err == nil && resolved.Identity != identity {
			err = integrity(fmt.Errorf("installed engine differs from the accepted replacement-pin engine"))
		}
		return resolved, err
	}
	version, err := VersionForPin(identity.Pin)
	if err != nil {
		return Resolved{}, err
	}
	if version != identity.Version || !validDigest(identity.ManifestDigest) {
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
	policy, err := r.Index(ctx, now)
	if err != nil {
		return Resolved{}, err
	}
	notices, err := Evaluate(policy.Index, identity.Version, now)
	if err != nil {
		return Resolved{}, err
	}
	notices = append(policy.Notices, notices...)
	cached, ok, err := r.findCached(ctx, identity, target)
	if err != nil {
		return Resolved{}, err
	}
	if ok {
		cached.Notices = notices
		return cached, nil
	}
	if r.config.Mode == toolchain.ModeLocal {
		return Resolved{}, &apierr.Error{Code: apierr.Unavailable, Message: "Exact verified engine is absent from the machine cache", Hint: "Use auto toolchain mode once to fetch the matching official release."}
	}
	release, err := r.fetchRelease(ctx, identity.Version, target)
	if err != nil {
		return Resolved{}, err
	}
	if identity.ManifestDigest != "" && digest(release.bundle.Manifest) != identity.ManifestDigest {
		return Resolved{}, integrity(fmt.Errorf("release manifest differs from accepted engine identity"))
	}
	resolved, err := r.StoreVerified(ctx, identity.Pin, release)
	resolved.Notices = notices
	return resolved, err
}

func (r *Resolver) findCached(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, bool, error) {
	if identity.ManifestDigest != "" {
		return r.Cached(ctx, identity, target)
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
		resolved, ok, err := r.Cached(ctx, identity, target)
		if err != nil || ok {
			return resolved, ok, err
		}
	}
	return Resolved{}, false, nil
}

// Cached re-verifies signatures, metadata and executable bytes; it grants no policy permission.
func (r *Resolver) Cached(ctx context.Context, identity engineid.Identity, target releaseasset.Target) (Resolved, bool, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, false, err
	}
	version, err := VersionForPin(identity.Pin)
	if err != nil {
		return Resolved{}, false, err
	}
	if version != identity.Version || !validDigest(identity.ManifestDigest) {
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
		return Resolved{}, false, nil
	}
	release, err := readCachedRelease(version, target, executable)
	var failure *apierr.Error
	if errors.Is(err, os.ErrNotExist) || errors.As(err, &failure) && failure.Code == apierr.IntegrityFailure {
		return Resolved{}, false, nil
	}
	if err != nil {
		return Resolved{}, false, err
	}
	if digest(release.bundle.Manifest) != identity.ManifestDigest {
		return Resolved{}, false, nil
	}
	return Resolved{Identity: identity, Target: target, AssetDigest: release.asset.Digest(), Executable: executable}, true, nil
}

func readCachedRelease(version string, target releaseasset.Target, executable string) (VerifiedRelease, error) {
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "verification.json"))
	if err != nil {
		return VerifiedRelease{}, err
	}
	var bundle ReleaseBundle
	if err := decodeUniqueJSON(metadata, &bundle); err != nil {
		return VerifiedRelease{}, integrity(errors.New("invalid cached release verification record"))
	}
	bundle.Asset, err = os.ReadFile(executable)
	if err != nil {
		return VerifiedRelease{}, err
	}
	return VerifyRelease(version, target, bundle)
}

// StoreVerified publishes a complete verified cache entry by an atomic directory rename.
func (r *Resolver) StoreVerified(ctx context.Context, pin engineid.Pin, release VerifiedRelease) (resolved Resolved, result error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	version, err := VersionForPin(pin)
	if err != nil {
		return Resolved{}, err
	}
	if version != release.version || release.asset.Name() == "" {
		return Resolved{}, integrity(fmt.Errorf("release capability does not match SDK pin"))
	}
	identity := engineid.Identity{Pin: pin, Version: version, ManifestDigest: digest(release.bundle.Manifest)}
	if cached, ok, err := r.Cached(ctx, identity, release.target); err != nil || ok {
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
		if cached, ok, cacheErr := r.Cached(ctx, identity, release.target); cacheErr != nil || ok {
			return cached, cacheErr
		}
		if err := os.RemoveAll(destination); err != nil {
			return Resolved{}, err
		}
		if err := os.Rename(stage, destination); err != nil {
			return Resolved{}, err
		}
	}
	return Resolved{Identity: identity, Target: release.target, AssetDigest: release.asset.Digest(), Executable: filepath.Join(destination, release.asset.Name())}, nil
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
