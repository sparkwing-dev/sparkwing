// Package engineid holds SDK pins, engine identities and version presentation values.
package engineid

import "time"

// SDKKind distinguishes the two pin formats without selecting a different engine.
type SDKKind string

const (
	GoSDK         SDKKind = "go"
	TypeScriptSDK SDKKind = "typescript"
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

// VersionInfo retains every published release, including releases beyond their support window.
type VersionInfo struct {
	Version      string    `json:"version"`
	PublishedAt  time.Time `json:"published_at"`
	SupportUntil time.Time `json:"support_until"`
}

// NoticeKind is stable presentation data shared by run output and controller API/dashboard readers.
type NoticeKind string

// Notice carries actionable policy text without turning a warning into an implicit revocation.
type Notice struct {
	Kind         NoticeKind `json:"kind"`
	Version      string     `json:"version,omitempty"`
	Reason       string     `json:"reason"`
	Hint         string     `json:"hint,omitempty"`
	SupportUntil *time.Time `json:"support_until,omitempty"`
	EnforceAt    *time.Time `json:"enforce_at,omitempty"`
}

const (
	NoticeSupportEnding     NoticeKind = "support_ending"
	NoticeSupportEnded      NoticeKind = "support_ended"
	NoticeEnforcement       NoticeKind = "version_enforcement"
	NoticeIndexFallback     NoticeKind = "version_index_fallback"
	NoticePinDisabled       NoticeKind = "engine_pin_disabled"
	NoticePolicyUnavailable NoticeKind = "version_policy_unavailable"
	NoticeUnclaimed         NoticeKind = "unclaimed_work"
	NoticeCreditHold        NoticeKind = "credit_hold"
	NoticeNoEligibleRunner  NoticeKind = "no_eligible_runner"
)
