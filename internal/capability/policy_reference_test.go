package capability

import "testing"

func TestReferenceTargetAdmission(t *testing.T) {
	for _, tc := range []struct {
		target                 PipelineReferenceTarget
		declaredAny, declTrust bool
		want                   bool
	}{
		{PipelineReferenceTarget{}, false, false, true},
		{PipelineReferenceTarget{FromAnyRun: true}, false, false, false},
		{PipelineReferenceTarget{FromAnyRun: true}, true, false, true},
		{PipelineReferenceTarget{FromAnyRun: true}, false, true, false},
		{PipelineReferenceTarget{FromProvenRun: true}, false, false, false},
		{PipelineReferenceTarget{FromProvenRun: true}, true, false, false},
		{PipelineReferenceTarget{FromProvenRun: true}, false, true, true},
		{PipelineReferenceTarget{FromAnyRun: true, FromProvenRun: true}, false, true, true},
		{PipelineReferenceTarget{FromAnyRun: true, FromProvenRun: true}, true, false, true},
	} {
		if got := tc.target.AdmittedBy(tc.declaredAny, tc.declTrust); got != tc.want {
			t.Errorf("%+v admitted by any=%v trusted=%v = %v, want %v", tc.target, tc.declaredAny, tc.declTrust, got, tc.want)
		}
	}
}
