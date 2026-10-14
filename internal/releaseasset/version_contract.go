package releaseasset

import "crypto/ed25519"

// NamesForVersion returns the closed executable set for a published release's asset format.
// Retained formats keep old permitted releases verifiable when today's Names matrix changes.
// Version policy, not absence from the current publishing matrix, decides whether a release runs.
func NamesForVersion(version string) ([]string, error) { panic("unimplemented: c3-engine") }

// NameForVersion validates the target against its release's retained matrix before naming it.
// It does not reject an older target merely because Target.Name no longer publishes it today.
func (target Target) NameForVersion(version string) (string, error) {
	panic("unimplemented: c3-engine")
}

// VerifyForVersion applies Verify's signature/digest checks with the retained release matrix.
// It requires exact closed manifest membership, including duplicate/missing/extra entry refusal.
// The engine separately checks the embedded binary version before any downloaded bytes execute.
func VerifyForVersion(publicKeys []ed25519.PublicKey, version string, manifest, manifestSignature []byte, target Target, asset, assetSignature []byte) (Verified, error) {
	panic("unimplemented: c3-engine")
}
