package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChildAwaitObserver_EvidenceWithoutErrors(t *testing.T) {
	obs := childAwaitObserver{startedAt: time.Now().Add(-2 * time.Second)}
	obs.observeStatus("queued")
	obs.observeStatus("running")

	got := obs.evidence()
	for _, want := range []string{"polls=2", "last_status=running", "waited=", "first_status_after="} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "store_errors") {
		t.Errorf("evidence %q reports errors when none were swallowed", got)
	}
}

// A child that has not been claimed yet has no runs row, which the loop
// must not report as a store fault: it is the normal state of every queued
// or still-compiling child.
func TestChildAwaitObserver_MissingRunRowIsNotAStoreError(t *testing.T) {
	var obs childAwaitObserver
	obs.observeMissing()
	obs.observeMissing()

	if obs.firstError() {
		t.Error("a missing runs row must not trip the swallowed-error log")
	}
	got := obs.evidence()
	if !strings.Contains(got, "polls_before_run_row=2") {
		t.Errorf("evidence %q does not count the polls before the run row appeared", got)
	}
	if strings.Contains(got, "store_errors") {
		t.Errorf("evidence %q counts a missing row as a store error", got)
	}
}

func TestChildAwaitObserver_EvidenceNamesFirstAndLastSwallowedError(t *testing.T) {
	var obs childAwaitObserver
	obs.observeError(errors.New("dial tcp: connection refused"))
	obs.observeError(errors.New("read: i/o timeout"))
	obs.observeError(errors.New("read: i/o timeout"))

	got := obs.evidence()
	for _, want := range []string{
		"polls=3",
		"last_status=none",
		"store_errors=3",
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
	obs.observeError(errors.New("store unavailable"))
	obs.observeStatus("running")

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
	obs.observeError(errors.New("store unavailable"))

	got := obs.evidence()
	if strings.Count(got, "store unavailable") != 1 {
		t.Errorf("evidence %q repeats the only swallowed error", got)
	}
}

func TestChildAwaitObserver_FirstErrorIsReportedOnce(t *testing.T) {
	var obs childAwaitObserver

	obs.observeError(errors.New("first"))
	if !obs.firstError() {
		t.Fatal("the first swallowed error should be worth logging")
	}
	obs.observeError(errors.New("second"))
	if obs.firstError() {
		t.Fatal("only the first swallowed error should be logged")
	}
}
