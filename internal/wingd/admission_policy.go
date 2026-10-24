package wingd

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// AdmissionPolicy is the daemon's local host-contention policy. A nil policy
// uses Classic; callers normally obtain one from ResolveAdmissionPolicy.
type AdmissionPolicy struct {
	Mode       admission.Mode
	Scheduling admission.SchedulingPolicy
}

func defaultAdmissionPolicy() AdmissionPolicy {
	return AdmissionPolicy{Mode: admission.ModeClassic}
}

func (c Config) admissionPolicy() AdmissionPolicy {
	if c.AdmissionPolicy == nil {
		return defaultAdmissionPolicy()
	}
	return *c.AdmissionPolicy
}

func (p AdmissionPolicy) interactiveBurstEligible(req *wingwire.AdmissionRequest, resources wingwire.HostResources) bool {
	if p.Mode == admission.ModeOff || resources.Cores <= 0 || req.ExpectedP99MS <= 0 {
		return false
	}
	class := admission.NormalizeWorkloadClass(admission.WorkloadClass(req.Class))
	if class != admission.ClassInteractive && class != admission.ClassCritical {
		return false
	}
	burst := p.Scheduling.Burst
	return burst.MaxCores > 0 && resources.Cores <= burst.MaxCores &&
		burst.MaxP99 > 0 && time.Duration(req.ExpectedP99MS)*time.Millisecond <= burst.MaxP99 &&
		req.SampleCount >= burst.MinSamples
}
