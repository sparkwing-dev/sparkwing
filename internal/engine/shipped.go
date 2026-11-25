package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// ShippedReleasesDirectory is where the Cloud runner image ships release files,
// laid out as <version>/<release file name>, exactly as the release publishes them.
const ShippedReleasesDirectory = "/usr/local/share/sparkwing/releases"

// safety: the directory grants no trust; its files pass VerifyRelease like a download, and any missing,
// unreadable, unverifiable or differently signed file yields false so the caller downloads instead.
func (r *Resolver) shippedRelease(identity engineid.Identity, target releaseasset.Target) (VerifiedRelease, bool) {
	if r.config.ShippedDirectory == "" || !releaseVersion(identity.Version) {
		return VerifiedRelease{}, false
	}
	name, err := target.NameForVersion(identity.Version)
	if err != nil {
		return VerifiedRelease{}, false
	}
	dir := filepath.Join(r.config.ShippedDirectory, identity.Version)
	var bundle ReleaseBundle
	for _, item := range []struct {
		name string
		body *[]byte
	}{{"SHA256SUMS", &bundle.Manifest}, {"SHA256SUMS.sig", &bundle.ManifestSignature}, {name + ".sig", &bundle.AssetSignature}, {name, &bundle.Asset}} {
		body, err := readShippedFile(filepath.Join(dir, item.name))
		if errors.Is(err, os.ErrNotExist) {
			return VerifiedRelease{}, false
		}
		if err != nil {
			slog.Default().Warn("engine: shipped release is unreadable; downloading instead", "version", identity.Version, "error", err)
			return VerifiedRelease{}, false
		}
		*item.body = body
	}
	verified, err := VerifyRelease(identity.Version, target, bundle, r.config.TrustedKeys)
	if err == nil && identity.ManifestDigest != "" && digest(bundle.Manifest) != identity.ManifestDigest {
		err = errors.New("release manifest differs from accepted engine identity")
	}
	if err != nil {
		slog.Default().Warn("engine: shipped release failed verification; downloading instead", "version", identity.Version, "error", err)
		return VerifiedRelease{}, false
	}
	return verified, true
}

// safety: a linked file could name anything the process can read; directories are not checked because the
// signatures reject any bytes a link substitutes.
func readShippedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}
