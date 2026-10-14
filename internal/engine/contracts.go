// Package engine selects the exact SDK-matched engine and owns verified machine caching.
package engine

import (
	"context"
	"net/http"
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

// ReadPin resolves a Go requirement or a TypeScript lockfile entry without running pipeline code.
// Package ranges require an exact lockfile resolution. Missing, conflicting or non-release
// pins return an actionable invalid_args error; no installed-engine version is substituted.
func ReadPin(checkout string, sdk SDKKind) (Pin, error) { panic("unimplemented: c3-engine") }

// VersionForPin maps Go vX.Y.Z and TypeScript X.Y.Z to exactly engine vX.Y.Z.
// A newer installed engine never satisfies a different pin.
func VersionForPin(pin Pin) (string, error) { panic("unimplemented: c3-engine") }

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
}

// VerifyRelease uses releaseauth.PublicKeys and releaseasset.VerifyForVersion, the shared release trust owners.
// It also checks exact manifest membership against releaseasset.NamesForVersion (no duplicate, missing or
// extra executable), requires the requested binary/OS/architecture, and reads embedded identity
// to require the requested release version
// without executing downloaded code. A failed check returns integrity_failure.
func VerifyRelease(version string, target releaseasset.Target, bundle ReleaseBundle) (VerifiedRelease, error) {
	panic("unimplemented: c3-engine")
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
type Resolver struct{ config Config }

// DefaultCacheDirectory returns os.UserCacheDir()/sparkwing/engines on the host OS.
// Every placement shares this default; no pipeline setting or new environment variable selects it.
func DefaultCacheDirectory() (string, error) { panic("unimplemented: c3-engine") }

// NewResolver uses DefaultCacheDirectory when no host/test override is supplied.
// The resulting directory is private; mode keeps the existing auto/local grammar.
func NewResolver(config Config) (*Resolver, error) { panic("unimplemented: c3-engine") }

// Resolve checks the current verified version policy, fetches the exact release only from GitHub
// Releases when absent, verifies it, and atomically publishes a reusable machine-cache entry.
// Target.Binary must be releaseasset.Sparkwing; Target.NameForVersion validates the release platform.
// The key is (release, binary, OS, architecture, manifest digest). Cached bytes are rechecked
// before execution; corrupt/missing entries are misses and unverified bytes never replace them.
// Every start checks policy, including cache hits. Index refresh follows limits.VersionIndexRefresh.
func (r *Resolver) Resolve(ctx context.Context, pin Pin, target releaseasset.Target) (Resolved, error) {
	panic("unimplemented: c3-engine")
}

// Ensure resolves a sealed identity on another executor and requires the same release manifest.
// A retry can change platform but cannot change the SDK pin or release; policy still applies.
// Target.Binary must be releaseasset.Sparkwing; controller/cache/runner assets cannot host a pipeline.
func (r *Resolver) Ensure(ctx context.Context, identity Identity, target releaseasset.Target) (Resolved, error) {
	panic("unimplemented: c3-engine")
}

// Cached returns only an integrity-checked cache entry; absence or damage is a miss.
// Cached never grants permission to execute: Resolve/Ensure must first enforce version policy.
func (r *Resolver) Cached(ctx context.Context, identity Identity, target releaseasset.Target) (Resolved, bool, error) {
	panic("unimplemented: c3-engine")
}

// StoreVerified publishes only verified bytes and retains signatures for future integrity checks.
// Concurrent installs may converge on the same cache entry; a partial write never becomes executable.
func (r *Resolver) StoreVerified(ctx context.Context, pin Pin, release VerifiedRelease) (Resolved, error) {
	panic("unimplemented: c3-engine")
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

// VerifyIndex checks releaseauth.PublicKeys signatures, unique version identities, required fields,
// VersionIndexFormat and restriction dates/reasons before decoding a usable policy.
// A support-window reduction or an unsigned policy cannot become cached authority.
func VerifyIndex(signed SignedIndex, now time.Time) (VerifiedIndex, error) {
	panic("unimplemented: c3-engine")
}

// Policy returns a copy for diagnostics and policy evaluation; callers cannot mutate verified state.
func (v VerifiedIndex) Policy() VersionIndex { panic("unimplemented: c3-engine") }

// IndexResult reports fallback explicitly so output and the dashboard can show the same notice.
type IndexResult struct {
	Index    VerifiedIndex
	Fallback bool
	Notices  []Notice
}

// Index fetches only VersionIndexURL and its detached signature after the short refresh interval.
// Failed fetch/verification keeps the last verified copy with no maximum fallback age.
// With no verified copy it returns apierr.Unavailable and a retry/connectivity hint; it never
// silently permits execution. Newly verified bytes replace cached policy only after validation.
// A lower PublishedAt or different bytes at the same PublishedAt cannot replace the last verified
// policy. An identical signed-body replay is harmless. First use has only the fetched signature
// as freshness evidence; no prior policy exists to establish a rollback floor.
func (r *Resolver) Index(ctx context.Context, now time.Time) (IndexResult, error) {
	panic("unimplemented: c3-engine")
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

// Evaluate refuses active revocations as apierr.VersionRevoked and releases below an effective
// minimum as apierr.VersionUnsupported, with signed reason and exactly UpgradeCommand as the fix.
// SupportUntil alone never refuses execution. Pending restrictions return notices before enforcement.
func Evaluate(index VerifiedIndex, version string, now time.Time) ([]Notice, error) {
	panic("unimplemented: c3-engine")
}

// EvaluateRunner enforces the same restrictions as Evaluate before new awards, with an automatic
// update/restart hint instead of a repository pin-upgrade hint. Existing claim renewals remain
// permitted under their own authority while the runner drains. This check never blocks fetching
// a verified replacement for an old or revoked runner.
func EvaluateRunner(index VerifiedIndex, version string, now time.Time) ([]Notice, error) {
	panic("unimplemented: c3-engine")
}
