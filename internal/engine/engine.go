// Package engine selects the exact SDK-matched engine and owns verified machine caching.
package engine

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
)

const (
	GoModule           = "github.com/sparkwing-dev/sparkwing"
	typeScriptPackage  = "@sparkwing/sdk"
	UpgradeCommand     = "sparkwing upgrade"
	VersionIndexFormat = 1
)

// Resolved includes the selected executable for this machine's platform.
type Resolved struct {
	Identity    engineid.Identity
	Target      releaseasset.Target
	AssetDigest string
	Executable  string
	Notices     []engineid.Notice
	trustedKeys []ed25519.PublicKey
}

// ReleaseBundle is untrusted until VerifyRelease checks it against the keys its caller supplies; the resolver supplies the production roots.
type ReleaseBundle struct {
	Manifest          []byte
	ManifestSignature []byte
	Asset             []byte
	AssetSignature    []byte
}

// VerifiedRelease cannot be constructed outside this package before verification.
type VerifiedRelease struct {
	version string
	target  releaseasset.Target
	asset   releaseasset.Verified
	bundle  ReleaseBundle
}

// Config supplies machine-owned storage and transport, never repository-selected trust roots or URLs.
type Config struct {
	CacheDirectory string
	Mode           toolchain.Mode
	HTTPClient     *http.Client
	BuildVersion   string
	LocalOnly      bool
	// TrustedKeys defaults to releaseauth.PublicKeys when nil. NewResolver copies every key.
	TrustedKeys []ed25519.PublicKey
}

// Resolver owns the version-index cache and verified executable cache for one machine.
type Resolver struct {
	config    Config
	indexMu   sync.Mutex
	index     VerifiedIndex
	checkedAt time.Time
	indexErr  error
	notice    sync.Once
	command   func(context.Context, string, string, ...string) error
}

// Restriction carries signed notice and enforcement dates; an empty Version means no restriction.
type Restriction struct {
	Version     string    `json:"version"`
	Reason      string    `json:"reason"`
	Security    bool      `json:"security"`
	AnnouncedAt time.Time `json:"announced_at"`
	EffectiveAt time.Time `json:"effective_at"`
}

// VersionIndex is signed as the exact JSON body bytes. Versions is append-only by release identity.
type VersionIndex struct {
	Format      int                    `json:"format"`
	PublishedAt time.Time              `json:"published_at"`
	Latest      string                 `json:"latest"`
	Minimum     Restriction            `json:"minimum"`
	Revoked     []Restriction          `json:"revoked"`
	Versions    []engineid.VersionInfo `json:"versions"`
}

// SignedIndex keeps the exact bytes and detached signature for verification and persistent fallback.
type SignedIndex struct {
	Body      []byte
	Signature []byte
}

// VerifiedIndex carries signed policy or the host development-build exemption.
type VerifiedIndex struct {
	index       VersionIndex
	signed      SignedIndex
	verifiedAt  time.Time
	development bool
	offline     bool
}

// IndexResult reports fallback explicitly so output and the dashboard can show the same notice.
type IndexResult struct {
	Index    VerifiedIndex
	Fallback bool
	Notices  []engineid.Notice
}
