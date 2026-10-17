package store

import (
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestBlobRecordsRemoteCacheResolvesVerifiedDefault(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		t.Run(string(kind), func(t *testing.T) {
			s, records, intent := blobRestoreFixture(t)
			reader := authorityRoot("remote-cache", "a")
			reader.Grants = []capability.Grant{capability.DataRead}
			reader.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
			authorityPersist(t, s, reader)
			key := plantBlobRestore(t, s, records, intent, kind, "main", restoreFacts("main", true), false)
			request := blob.RecordLookup{Authority: blob.Authority{CapabilityRef: reader.Prefix}, Key: key, VerifiedDefaultOnly: true}
			request.Key.Scope.Ref = blob.RefScope{}
			hit, err := records.LookupRecord(t.Context(), request)
			if err != nil || !hit.Found || !hit.VerifiedDefault || hit.Record.Key != key || hit.Record.Digest != intent.Digest {
				t.Fatalf("server-selected default: %+v %v", hit, err)
			}
			property := func(value string, field uint8) bool {
				facts := restoreFacts("main", true)
				switch field % 6 {
				case 0:
					facts.DefaultBranchVerified = false
				case 1:
					facts.Scope.Verified = false
					facts.DefaultBranchVerified = false
				case 2:
					facts.AppDeliveryKey = ""
				case 3:
					facts.SHA = ""
				case 4:
					facts.DefaultBranch = "foreign:" + value
				case 5:
					facts.Scope.Ref = "foreign:" + value
				}
				plantBlobRestore(t, s, records, intent, kind, "main", facts, false)
				hit, _ := records.LookupRecord(t.Context(), request)
				return !hit.Found && !hit.VerifiedDefault
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
