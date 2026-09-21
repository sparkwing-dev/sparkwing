package sparkwing

import "context"

// Admission is the resource charge the scheduler reserved for the job that is
// running. A step sizing its own parallelism reads this rather than the
// machine: a host runs several jobs at once, so the machine reports what the
// step can see while this reports what it was given.
//
// Distinct from [AdmissionClass], which is what a pipeline declares about its
// latency expectations going in. This is what the scheduler decided coming
// back.
type Admission struct {
	// Cores is the processor share reserved for the job. It is fractional
	// because a share is what a scheduler divides, not a count of
	// processors, so a caller wanting a worker count rounds it itself.
	Cores float64

	// MemoryBytes is the memory reserved for the job.
	MemoryBytes int64

	// Source is how the charge was reached: measured from this job's own
	// history, taken from a pin the pipeline declared, or defaulted before
	// any history existed. A step that sizes itself against a default is
	// sizing against the scheduler's opening guess rather than an
	// observation, so it is worth branching on.
	Source string
}

// Admitted returns the charge the orchestrator reserved for the current job,
// or nil where nothing installed one: inside Plan, in a test that installed
// none, and on any path not carrying the dispatch context. A caller reading
// nil has been told nothing and sizes itself as it otherwise would.
func Admitted(ctx context.Context) *Admission {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(keyAdmitted).(*Admission)
	return v
}

type keyAdmittedType struct{}

var keyAdmitted = keyAdmittedType{}
