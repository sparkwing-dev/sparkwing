package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type cacheVerification struct {
	executable     string
	version        string
	target         releaseasset.Target
	manifestDigest string
	assetDigest    string
	metadataDigest string
}

func (v *cacheVerification) check(ctx context.Context) error {
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(v.executable), "verification.json"))
	if err != nil {
		return err
	}
	if digest(metadata) != v.metadataDigest {
		return errors.New("cached release verification record changed")
	}
	actual, err := fullExecutableDigest(ctx, v.executable)
	if err != nil {
		return err
	}
	if actual != v.assetDigest {
		return errors.New("cached engine bytes differ from the verified release")
	}
	return nil
}

type cacheVerificationKey struct {
	executable string
	version    string
	target     releaseasset.Target
	trust      string
}

var verifiedReleases struct {
	sync.Mutex
	entries map[cacheVerificationKey]*cacheVerification
}

func (r *Resolver) verificationKey(version string, target releaseasset.Target, executable string) cacheVerificationKey {
	var trust strings.Builder
	for _, key := range r.config.TrustedKeys {
		fmt.Fprintf(&trust, "%d:%x;", len(key), key)
	}
	return cacheVerificationKey{executable: executable, version: version, target: target, trust: digest([]byte(trust.String()))}
}

func (r *Resolver) verifyCached(ctx context.Context, version string, target releaseasset.Target, executable string) (*cacheVerification, error) {
	key := r.verificationKey(version, target, executable)
	verifiedReleases.Lock()
	if verified := verifiedReleases.entries[key]; verified != nil {
		verifiedReleases.Unlock()
		return verified, verified.check(ctx)
	}
	defer verifiedReleases.Unlock()
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "verification.json"))
	if err != nil {
		return nil, err
	}
	release, err := verifyCachedRecord(version, target, executable, r.config.TrustedKeys, metadata)
	if err != nil {
		return nil, err
	}
	verified := &cacheVerification{executable: executable, version: version, target: target, manifestDigest: digest(release.bundle.Manifest), assetDigest: release.asset.Digest(), metadataDigest: digest(metadata)}
	// safety: only the verification record read with these authenticated bytes can be reused.
	if err := verified.check(ctx); err != nil {
		return nil, err
	}
	rememberVerifiedLocked(key, verified)
	return verified, nil
}

func rememberVerifiedLocked(key cacheVerificationKey, verified *cacheVerification) {
	if verifiedReleases.entries == nil {
		verifiedReleases.entries = make(map[cacheVerificationKey]*cacheVerification)
	}
	verifiedReleases.entries[key] = verified
}

func cachedIntegrity(executable string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &apierr.Error{Code: hosterr.IntegrityFailure, Message: fmt.Sprintf("Cached engine %s failed verification: %v", executable, err), Hint: fmt.Sprintf("Remove the release directory %q, then retry the original command to download and verify the engine again.", filepath.Dir(executable))}
}
