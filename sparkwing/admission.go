package sparkwing

import "context"

// AdmissionClass describes the latency expectations of a pipeline without
// imposing an absolute queue order. Most pipelines should leave it unset and
// let Sparkwing infer the class from the trigger.
type AdmissionClass string

const (
	AdmissionCritical    AdmissionClass = "critical"
	AdmissionInteractive AdmissionClass = "interactive"
	AdmissionNormal      AdmissionClass = "normal"
	AdmissionBatch       AdmissionClass = "batch"
)

// AdmissionClass sets the pipeline's local contention class. Critical is for
// rare operator-critical work; pre-commit and pre-push hooks are inferred as
// interactive without requiring this call.
func (p *Plan) AdmissionClass(class AdmissionClass) *Plan {
	switch class {
	case AdmissionCritical, AdmissionInteractive, AdmissionNormal, AdmissionBatch:
	default:
		panic("sparkwing: unknown admission class " + string(class))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.admissionClass = class
	return p
}

// AdmissionClassValue returns the plan's explicit local contention class.
func (p *Plan) AdmissionClassValue() AdmissionClass {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admissionClass
}

// Admission is the resource share the scheduler reserved for the running job.
// A step sizing its own parallelism reads this rather than the machine,
// because a host runs several jobs at once.
type Admission struct {
	Cores       float64
	MemoryBytes int64
}

// Admitted reports the share the scheduler reserved for the running job. The
// second result is false where nothing reserved anything -- inside Plan, in a
// test that installed none, and on any path not carrying the dispatch context.
// A caller told nothing sizes itself as it otherwise would.
func Admitted(ctx context.Context) (Admission, bool) {
	if ctx == nil {
		return Admission{}, false
	}
	a, ok := ctx.Value(keyAdmission).(Admission)
	return a, ok
}

type keyAdmissionType struct{}

var keyAdmission = keyAdmissionType{}
