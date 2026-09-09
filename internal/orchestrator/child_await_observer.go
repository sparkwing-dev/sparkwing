package orchestrator

import (
	"fmt"
	"strings"
)

// childAwaitObserver records what a RunAndAwait child-wait loop actually
// saw. The loop re-polls past a GetRun error rather than returning it, so
// without this a store that errors for a minute produces a bare
// "waiting for child <id>: context deadline exceeded" and no evidence of
// what the parent observed.
type childAwaitObserver struct {
	polls      int
	lastStatus string
	errCount   int
	firstErr   error
	lastErr    error
}

// observe records one poll: either a child status or the error that
// replaced it.
func (o *childAwaitObserver) observe(status string, err error) {
	o.polls++
	if err != nil {
		o.errCount++
		if o.firstErr == nil {
			o.firstErr = err
		}
		o.lastErr = err
		return
	}
	if status != "" {
		o.lastStatus = status
	}
}

// firstError reports whether the poll just observed was the first to
// swallow a GetRun error, so the caller logs it once instead of per poll.
func (o *childAwaitObserver) firstError() bool {
	return o.errCount == 1
}

// evidence renders what the wait saw, for the timeout error.
func (o *childAwaitObserver) evidence() string {
	status := o.lastStatus
	if status == "" {
		status = "none"
	}
	parts := []string{
		fmt.Sprintf("polls=%d", o.polls),
		fmt.Sprintf("last_status=%s", status),
	}
	if o.errCount > 0 {
		parts = append(parts,
			fmt.Sprintf("getrun_errors=%d", o.errCount),
			fmt.Sprintf("first_getrun_error=%q", o.firstErr))
		if o.lastErr.Error() != o.firstErr.Error() {
			parts = append(parts, fmt.Sprintf("last_getrun_error=%q", o.lastErr))
		}
	}
	return strings.Join(parts, " ")
}
