package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

// childAwaitObserver records what a RunAndAwait child-wait loop actually
// saw. The loop re-polls past a GetRun error rather than returning it, so
// without this a store that errors for a minute produces a bare
// "waiting for child <id>: context deadline exceeded" and no evidence of
// what the parent observed.
type childAwaitObserver struct {
	startedAt time.Time

	polls        int
	notFound     int
	lastStatus   string
	firstSeenAt  time.Time
	errCount     int
	firstErr     error
	lastErr      error
	admissionOff bool
}

// observeMissing records a poll that found no runs row. That is a healthy
// answer while the child is queued or still compiling its pipeline binary:
// the row appears only once a consumer claims the run.
func (o *childAwaitObserver) observeMissing() {
	o.polls++
	o.notFound++
}

// observeError records a poll whose store error the loop swallowed.
func (o *childAwaitObserver) observeError(err error) {
	o.polls++
	o.errCount++
	if o.firstErr == nil {
		o.firstErr = err
	}
	o.lastErr = err
}

// observeStatus records a poll that read the child's status.
func (o *childAwaitObserver) observeStatus(status string) {
	o.polls++
	if status == "" {
		return
	}
	if o.lastStatus == "" {
		o.firstSeenAt = time.Now()
	}
	o.lastStatus = status
}

// firstError reports whether the poll just observed was the first to
// swallow a store error, so the caller logs it once instead of per poll.
func (o *childAwaitObserver) firstError() bool {
	return o.errCount == 1
}

// evidence renders what the wait saw, for the timeout error. The timings
// are what separate "the child finished well before the deadline and we
// were slow to notice" from "the deadline simply arrived".
func (o *childAwaitObserver) evidence() string {
	status := o.lastStatus
	if status == "" {
		status = "none"
	}
	parts := []string{
		fmt.Sprintf("polls=%d", o.polls),
		fmt.Sprintf("last_status=%s", status),
	}
	if !o.startedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("waited=%s", time.Since(o.startedAt).Round(time.Millisecond)))
		if !o.firstSeenAt.IsZero() {
			parts = append(parts, fmt.Sprintf("first_status_after=%s",
				o.firstSeenAt.Sub(o.startedAt).Round(time.Millisecond)))
		}
	}
	if o.notFound > 0 {
		parts = append(parts, fmt.Sprintf("polls_before_run_row=%d", o.notFound))
	}
	if o.admissionOff {
		parts = append(parts, "admission_paused=true")
	}
	if o.errCount > 0 {
		parts = append(parts,
			fmt.Sprintf("store_errors=%d", o.errCount),
			fmt.Sprintf("first_store_error=%q", o.firstErr))
		if o.errCount > 1 {
			parts = append(parts, fmt.Sprintf("last_store_error=%q", o.lastErr))
		}
	}
	return strings.Join(parts, " ")
}
