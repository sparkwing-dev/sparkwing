package sparkwing

// Outcome is a terminal node state.
type Outcome string

const (
	Success   Outcome = "success"
	Failed    Outcome = "failed"
	Satisfied Outcome = "satisfied"
	Cached    Outcome = "cached"
	Skipped   Outcome = "skipped"
	Cancelled Outcome = "cancelled"

	// SkippedConcurrent records a full concurrency group with OnLimit [Skip].
	SkippedConcurrent Outcome = "skipped-concurrent"

	// Superseded records eviction by a concurrency group's [CancelOthers] policy.
	Superseded Outcome = "superseded"
)

// Terminal reports true for Outcome values.
func (o Outcome) Terminal() bool { return true }

// Paused is the debug-pause status, outside the terminal Outcome set.
const Paused = "paused"

// OK reports whether the outcome satisfies downstream dependencies.
func (o Outcome) OK() bool {
	switch o {
	case Success, Satisfied, Cached, Skipped, SkippedConcurrent:
		return true
	}
	return false
}
