package controller

import (
	"encoding/hex"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDataRecordRefGrantPreservesTrustProperty(t *testing.T) {
	property := func(name [16]byte, verified bool) bool {
		ref := blob.RefScope{Ref: hex.EncodeToString(name[:]), Verified: verified}
		if !verified {
			ref.Writer = string(fixtureWriter)
		}
		grant := ref.CapabilityScope()
		scope := &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:1", WriteRef: grant, ReadRefs: []string{grant}}
		bound := boundData{authorized: capability.Authorized{
			Principal: capability.Principal{Capability: capability.Capability{Data: scope}},
			Target:    capability.Target{Team: "a"},
		}}
		authority := blob.Authority{Repo: "gh:1", Ref: blob.RefFacts{Scope: ref}, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Unit: runspec.Optional[blob.UploadUnit]{Set: true}}}}
		key := blob.RecordKey{Team: "a", Kind: blob.KindBinary, Scope: blob.Scope{Repo: "gh:1", Ref: ref}, Name: "binary"}
		for _, write := range []bool{false, true} {
			if err := bound.recordScope(authority, key, write); err != nil {
				t.Logf("exact ref refused: verified=%t write=%t: %v", verified, write, err)
				return false
			}
			forged := key
			forged.Scope.Ref.Verified = !verified
			if err := bound.recordScope(authority, forged, write); err == nil {
				t.Logf("trust crossed: verified=%t write=%t", verified, write)
				return false
			}
			forged = key
			forged.Scope.Ref.Ref += "-other"
			if err := bound.recordScope(authority, forged, write); err == nil {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(107))}); err != nil {
		t.Fatal(err)
	}
}
