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

// Pin records the SDK requirement, applicable replacement and source files.
type Pin struct {
	SDK          SDKKind `json:"sdk"`
	Version      string  `json:"version"`
	Manifest     string  `json:"manifest"`
	Lockfile     string  `json:"lockfile,omitempty"`
	InputsDigest string  `json:"inputs_digest"`
	Replacement  string  `json:"replacement,omitempty"`
}

// Identity binds the selected engine to SDK inputs in the accepted run seal.
type Identity struct {
	Pin            Pin    `json:"pin"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifest_digest"`
}

// Resolved includes the selected executable for this machine's platform.
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
type Config struct {
	CacheDirectory string
	Mode           toolchain.Mode
	HTTPClient     *http.Client
	BuildVersion   string
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

// VersionInfo retains every published release, including releases beyond their support window.
type VersionInfo struct {
	Version      string    `json:"version"`
	PublishedAt  time.Time `json:"published_at"`
	SupportUntil time.Time `json:"support_until"`
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

// VerifiedIndex carries signed policy or the host development-build exemption.
type VerifiedIndex struct {
	index       VersionIndex
	signed      SignedIndex
	verifiedAt  time.Time
	development bool
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
	NoticePinDisabled   NoticeKind = "engine_pin_disabled"
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
