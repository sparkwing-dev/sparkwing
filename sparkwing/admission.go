package sparkwing

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
