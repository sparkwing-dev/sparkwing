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
		{PipelineReferenceTarget{FromTrustedRun: true}, false, false, false},
		{PipelineReferenceTarget{FromTrustedRun: true}, true, false, false},
		{PipelineReferenceTarget{FromTrustedRun: true}, false, true, true},
		{PipelineReferenceTarget{FromAnyRun: true, FromTrustedRun: true}, false, true, true},
		{PipelineReferenceTarget{FromAnyRun: true, FromTrustedRun: true}, true, false, true},
	} {
		if got := tc.target.AdmittedBy(tc.declaredAny, tc.declTrust); got != tc.want {
			t.Errorf("%+v admitted by any=%v trusted=%v = %v, want %v", tc.target, tc.declaredAny, tc.declTrust, got, tc.want)
		}
	}
}
