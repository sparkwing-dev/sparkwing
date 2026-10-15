package sparkwing

// Outcome is a persisted terminal job verdict.
type Outcome string

const (
	Success    Outcome = "success"
	Failed     Outcome = "failed"
	Cancelled  Outcome = "cancelled"
	Skipped    Outcome = "skipped"
	Superseded Outcome = "superseded"
)

// Terminal reports whether the verdict belongs to the terminal vocabulary.
func (o Outcome) Terminal() bool {
	switch o {
	case Success, Failed, Cancelled, Skipped, Superseded, Memoized:
		return true
	}
	return false
}

// OK reports whether the verdict satisfies a successful dependency.
func (o Outcome) OK() bool { return o == Success || o == Memoized || o == Skipped }
