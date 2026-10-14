package engine

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
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
	Before  Pin
	After   Pin
	Edits   []PinEdit
	Engine  Identity
	Notices []Notice
}

// UpgradeVerifier checks dependency resolution, Go compilation or TypeScript type checking,
// then describe/plan with the exact selected engine. It never executes job bodies.
type UpgradeVerifier interface {
	Verify(context.Context, string, Resolved) error
}

// PrepareUpgrade computes edits with native Go/npm pin and lockfile semantics in a private staging
// checkout. It preserves unrelated content and refuses a revoked or unsupported target.
func (r *Resolver) PrepareUpgrade(ctx context.Context, request UpgradeRequest) (UpgradePlan, error) {
	panic("unimplemented: c3-engine")
}

// UpgradeResult reports verification separately from a dry-run preview.
type UpgradeResult struct {
	Plan     UpgradePlan
	Applied  bool
	Verified bool
	DryRun   bool
}

// Upgrade verifies the staged pin changes before applying them, rechecking BeforeDigest on each
// input. A failed check leaves original pins unchanged. A dry run reports only the plan.
// It updates repo SDK pins; the machine software updater owns installed CLI/runner executables.
func (r *Resolver) Upgrade(ctx context.Context, request UpgradeRequest, verifier UpgradeVerifier) (UpgradeResult, error) {
	panic("unimplemented: c3-engine")
}

// SoftwareUpdate keeps a verified runner replacement separate from an SDK-matched pipeline engine.
type SoftwareUpdate struct {
	CurrentVersion string
	Version        string
	Release        VerifiedRelease
	Required       bool
	Notices        []Notice
}

// PrepareRunnerUpdate applies the signed minimum/revocation policy to the runner itself, then
// obtains the latest permitted runner asset through the same GitHub-only verification/cache owner.
// Target.Binary must be releaseasset.SparkwingRunner; Target.NameForVersion validates the release platform.
// A denial for currentVersion marks Required but cannot stop replacement fetching. Only the
// replacement must pass current execution policy. Failures carry automatic update/restart guidance.
// It runs on startup and policy refresh. A refused old runner takes no new claims but renews its
// existing claims until the update coordinator has drained them.
func (r *Resolver) PrepareRunnerUpdate(ctx context.Context, currentVersion string, target releaseasset.Target) (SoftwareUpdate, error) {
	panic("unimplemented: c3-engine")
}

// Version returns the release version whose embedded executable identity was verified.
func (v VerifiedRelease) Version() string { panic("unimplemented: c3-engine") }

// Target returns the exact platform and binary verified against the closed release matrix.
func (v VerifiedRelease) Target() releaseasset.Target { panic("unimplemented: c3-engine") }

// Asset supplies immutable verified bytes to the native installer without bypassing verification.
func (v VerifiedRelease) Asset() releaseasset.Verified { panic("unimplemented: c3-engine") }
