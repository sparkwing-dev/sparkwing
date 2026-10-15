package engine

import (
	"context"
)

// UpgradeRequest chooses an exact release or the signed index's latest permitted release.
// DryRun resolves and reports changes without editing pins, installing software or restarting services.
type UpgradeRequest struct {
	Checkout string
	SDK      SDKKind
	Version  string
	DryRun   bool
}

// PinEdit preserves the source digest so an upgrade cannot overwrite concurrent user edits.
// It covers the manifest and native dependency lockfiles as one reviewed change set.
type PinEdit struct {
	Path         string
	BeforeDigest string
	After        []byte
}

// UpgradePlan contains concrete pin edits and the matching verified engine selection.
type UpgradePlan struct {
	Before       Pin
	After        Pin
	Edits        []PinEdit
	Engine       Identity
	Notices      []Notice
	inputDigests map[string]string
}

// UpgradeVerifier checks dependency resolution, Go compilation or TypeScript type checking,
// then describe/plan with the exact selected engine. It never executes job bodies.
type UpgradeVerifier interface {
	Verify(context.Context, string, Resolved) error
}

// UpgradeResult reports verification separately from a dry-run preview.
type UpgradeResult struct {
	Plan     UpgradePlan
	Applied  bool
	Verified bool
	DryRun   bool
}

// SoftwareUpdate keeps a verified runner replacement separate from an SDK-matched pipeline engine.
type SoftwareUpdate struct {
	CurrentVersion string
	Version        string
	Release        VerifiedRelease
	Required       bool
	Notices        []Notice
}
