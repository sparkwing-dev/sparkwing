package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

func TestChildAwaitObserver_EvidenceWithoutErrors(t *testing.T) {
	var obs childAwaitObserver
	obs.observe("queued", nil)
	obs.observe("running", nil)

	got := obs.evidence()
	if !strings.Contains(got, "polls=2") {
		t.Errorf("evidence %q does not report the poll count", got)
	}
	if !strings.Contains(got, "last_status=running") {
		t.Errorf("evidence %q does not report the last observed child status", got)
	}
	if strings.Contains(got, "getrun_errors") {
		t.Errorf("evidence %q reports errors when none were swallowed", got)
	}
}

func TestChildAwaitObserver_EvidenceNamesFirstAndLastSwallowedError(t *testing.T) {
	var obs childAwaitObserver
	obs.observe("", errors.New("dial tcp: connection refused"))
	obs.observe("", errors.New("read: i/o timeout"))
	obs.observe("", errors.New("read: i/o timeout"))

	got := obs.evidence()
	for _, want := range []string{
		"polls=3",
		"last_status=none",
		"getrun_errors=3",
		"dial tcp: connection refused",
		"read: i/o timeout",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence %q is missing %q", got, want)
		}
	}
}

// A store that errors for a minute and then answers is the case the ticket
// describes: the last status matters as much as the errors that preceded it.
func TestChildAwaitObserver_EvidenceKeepsBothErrorsAndLaterStatus(t *testing.T) {
	var obs childAwaitObserver
	obs.observe("", errors.New("store unavailable"))
	obs.observe("running", nil)

	got := obs.evidence()
	if !strings.Contains(got, "last_status=running") {
		t.Errorf("evidence %q lost the status observed after the error", got)
	}
	if !strings.Contains(got, "store unavailable") {
		t.Errorf("evidence %q lost the swallowed error", got)
	}
}

func TestChildAwaitObserver_EvidenceSingleErrorIsNotRepeated(t *testing.T) {
	var obs childAwaitObserver
	obs.observe("", errors.New("store unavailable"))

	got := obs.evidence()
	if strings.Count(got, "store unavailable") != 1 {
		t.Errorf("evidence %q repeats the only swallowed error", got)
	}
}

func TestChildAwaitObserver_FirstErrorIsReportedOnce(t *testing.T) {
	var obs childAwaitObserver
	if obs.observe("", errors.New("first")); !obs.firstError() {
		t.Fatal("the first swallowed error should be worth logging")
	}
	if obs.observe("", errors.New("second")); obs.firstError() {
		t.Fatal("only the first swallowed error should be logged")
	}
}
