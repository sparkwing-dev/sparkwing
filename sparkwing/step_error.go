package sparkwing

import "fmt"

// StepError attributes a failure to a step while preserving its underlying cause.
type StepError struct {
	StepID string
	Cause  error
}

func (e *StepError) Error() string {
	if e == nil || e.Cause == nil {
		return ""
	}
	return fmt.Sprintf("step %q: %s", e.StepID, e.Cause.Error())
}

func (e *StepError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
