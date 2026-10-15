// Package engine selects the exact SDK-matched engine and owns verified machine caching.
package engine

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
)

// SDKKind distinguishes the two pin formats without selecting a different engine.
type SDKKind string

const (
	GoSDK                    SDKKind = "go"
	TypeScriptSDK            SDKKind = "typescript"
	GoModule                         = "github.com/sparkwing-dev/sparkwing"
	TypeScriptPackage                = "@sparkwing/sdk"
	GitHubReleaseBase                = "https://github.com/sparkwing-dev/sparkwing/releases/download"
	VersionIndexURL                  = "https://sparkwing.dev/versions.json"
	VersionIndexSignatureURL         = "https://sparkwing.dev/versions.json.sig"
	UpgradeCommand                   = "sparkwing upgrade"
	VersionIndexFormat               = 1
)

// Pin records the exact installed SDK version and the files that establish it.
// Version is vMAJOR.MINOR.PATCH for Go and MAJOR.MINOR.PATCH for TypeScript,
// with matching prerelease suffixes retained. File paths are checkout-relative.
type Pin struct {
	SDK          SDKKind `json:"sdk"`
	Version      string  `json:"version"`
	Manifest     string  `json:"manifest"`
	Lockfile     string  `json:"lockfile,omitempty"`
	InputsDigest string  `json:"inputs_digest"`
}

// Identity is portable across executor platforms and survives retry in the accepted run seal.
// ManifestDigest binds the complete verified release manifest, not a machine executable path.
type Identity struct {
	Pin            Pin    `json:"pin"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifest_digest"`
}

// Resolved includes the executable verified for this machine's exact platform.
// Executable is local runtime data and must not enter a portable run seal.
type Resolved struct {
	Identity    Identity
	Target      releaseasset.Target
	AssetDigest string
	Executable  string
	Notices     []Notice
}

// ReleaseBundle is untrusted until VerifyRelease checks the shared release trust roots.
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
// Empty CacheDirectory uses DefaultCacheDirectory; an override belongs only to the host or tests.
// ModeLocal may use a verified matching cached executable but never downloads an engine asset.
type Config struct {
	CacheDirectory string
	Mode           toolchain.Mode
	HTTPClient     *http.Client
}

// Resolver owns the version-index cache and verified executable cache for one machine.
type Resolver struct {
	config    Config
	indexMu   sync.Mutex
	index     VerifiedIndex
	checkedAt time.Time
	indexErr  error
	command   func(context.Context, string, string, ...string) error
}

// SDKPlatform declares language support independently of the engine's release asset platform matrix.
type SDKPlatform struct {
	SDK  SDKKind
	GOOS string
}

// SDKPlatforms includes TypeScript on Windows because hello supplies the bearer without a preload.
var SDKPlatforms = []SDKPlatform{
	{GoSDK, "linux"},
	{GoSDK, "darwin"},
	{GoSDK, "windows"},
	{TypeScriptSDK, "linux"},
	{TypeScriptSDK, "darwin"},
	{TypeScriptSDK, "windows"},
}

// VersionInfo retains every published release, including releases beyond their support window.
// SupportUntil is PublishedAt plus limits.ControllerCompatibilityMonths calendar months.
type VersionInfo struct {
	Version      string    `json:"version"`
	PublishedAt  time.Time `json:"published_at"`
	SupportUntil time.Time `json:"support_until"`
}

// Restriction carries signed notice and enforcement dates; an empty Version means no restriction.
// A security reason is required for enforcement affecting a release still in its support window.
// Emergency revocations may enforce immediately; other changes announce weeks before EffectiveAt.
type Restriction struct {
	Version     string    `json:"version"`
	Reason      string    `json:"reason"`
	Security    bool      `json:"security"`
	AnnouncedAt time.Time `json:"announced_at"`
	EffectiveAt time.Time `json:"effective_at"`
}

// VersionIndex is signed as the exact JSON body bytes. Versions is append-only by release identity.
// Minimum applies to engines and runner software. Revocations name individual versions.
// Unknown additive fields may be ignored after signature verification; duplicate keys are refused.
type VersionIndex struct {
	Format      int           `json:"format"`
	PublishedAt time.Time     `json:"published_at"`
	Latest      string        `json:"latest"`
	Minimum     Restriction   `json:"minimum"`
	Revoked     []Restriction `json:"revoked"`
	Versions    []VersionInfo `json:"versions"`
}

// SignedIndex keeps the exact bytes and detached signature for verification and persistent fallback.
type SignedIndex struct {
	Body      []byte
	Signature []byte
}

// VerifiedIndex is obtainable only by checking the shared releaseauth trust roots.
// Its zero value is invalid and every policy consumer must reject it.
type VerifiedIndex struct {
	index      VersionIndex
	signed     SignedIndex
	verifiedAt time.Time
}

// IndexResult reports fallback explicitly so output and the dashboard can show the same notice.
type IndexResult struct {
	Index    VerifiedIndex
	Fallback bool
	Notices  []Notice
}

// NoticeKind is stable presentation data shared by run output and controller API/dashboard readers.
type NoticeKind string

const (
	NoticeSupportEnding NoticeKind = "support_ending"
	NoticeSupportEnded  NoticeKind = "support_ended"
	NoticeEnforcement   NoticeKind = "version_enforcement"
	NoticeIndexFallback NoticeKind = "version_index_fallback"
)

// Notice carries actionable policy text without turning a warning into an implicit revocation.
type Notice struct {
	Kind         NoticeKind `json:"kind"`
	Version      string     `json:"version,omitempty"`
	Reason       string     `json:"reason"`
	Hint         string     `json:"hint,omitempty"`
	SupportUntil *time.Time `json:"support_until,omitempty"`
	EnforceAt    *time.Time `json:"enforce_at,omitempty"`
}
