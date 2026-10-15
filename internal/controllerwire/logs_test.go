package controllerwire

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPublicLogRecordPreservesTaggedSubjects(t *testing.T) {
	for _, subject := range []blob.LogSubject{
		{Kind: blob.LogJobAttempt, Team: "t1", RunID: "r1", Attempt: &runspec.AttemptIdentity{Team: "t1", RunID: "r1", NodeID: "build", Attempt: 2, Generation: 3}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 2}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 2}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 2, Source: &blob.LogSource{NodeID: "build", Generation: 1}}},
	} {
		if err := (LogRecord{RunID: "r1", Subject: &subject, LineNo: 1, Line: "output"}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (LogRecord{RunID: "r1", Event: "run_finished", Line: "success"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (LogRecord{RunID: "r1", Event: "step_finished", JobID: "build", StepID: "compile", Line: "success"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicLogRecordRejectsMalformedSubjects(t *testing.T) {
	for _, subject := range []blob.LogSubject{
		{Kind: "unknown", Team: "t1", RunID: "r1"},
		{Kind: blob.LogJobAttempt, Team: "t1", RunID: "r1"},
		{Kind: blob.LogJobAttempt, Team: "t1", RunID: "r1", Attempt: &runspec.AttemptIdentity{Team: "other", RunID: "r1", NodeID: "build", Attempt: 1, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1"},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogPlan}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "r1", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1, Source: &blob.LogSource{NodeID: "build", Generation: 1}}},
	} {
		if err := (LogRecord{RunID: "r1", Subject: &subject, LineNo: 1}).Validate(); err == nil {
			t.Fatalf("accepted %+v", subject)
		}
	}
}

func TestLogSearchRequiresProgressEvenWithoutMatches(t *testing.T) {
	if err := (LogSearchPage{Truncated: true, NextCursor: "next"}).Validate("old"); err != nil {
		t.Fatal(err)
	}
	for _, page := range []LogSearchPage{{Truncated: true}, {Truncated: true, NextCursor: "same"}, {NextCursor: "unexpected"}, {Matches: []LogRecord{{RunID: "r1", Event: "run_finished"}}}} {
		if err := page.Validate("same"); err == nil {
			t.Fatalf("accepted %+v", page)
		}
	}
}
