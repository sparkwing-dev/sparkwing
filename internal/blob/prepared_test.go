package blob

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPreparedUploadRequiresPlanningOwner(t *testing.T) {
	for _, kind := range []UploadUnitKind{UploadPlanUnit, UploadTriggerUnit, UploadEvalUnit} {
		i := UploadIdentity{Kind: UploadOwnerUnit, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: "cap", Unit: runspec.Optional[UploadUnit]{Set: true, Value: UploadUnit{Kind: kind, ID: "run", Generation: 1}}}
		a := Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Upload: runspec.Optional[UploadIdentity]{Set: true, Value: i}}
		err := AuthorizeUpload(a, i, KindPrepared)
		if (err == nil) != (kind != UploadEvalUnit) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
