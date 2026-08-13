package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func fireResults() []githooks.FireResult {
	return []githooks.FireResult{
		{Repo: "/code/xwing", Verdict: githooks.FireRefused, Hook: "/code/xwing/.git/hooks/pre-commit"},
		{Repo: "/code/toolbox", Verdict: githooks.FireAccepted, Detail: "the commit landed"},
		{
			Repo:    "/code/sparkwing-platform",
			Verdict: githooks.FireBorrowed,
			Hook:    "/code/sparkwing/.git/hooks/pre-commit",
			Detail:  "the gate that refused lives in /code/sparkwing/.git/hooks",
		},
		{Repo: "/code/quiet", Verdict: githooks.FireUndeclared, Detail: "no pipeline declares pre-commit"},
	}
}

// A repo with no gate to fire is not a failure; every other verdict short of a
// refusal by the repo's own gate is, including the ones that mean the question
// went unanswered.
func TestUnenforcedResults_CountsEverythingButARefusalAndNothingToFire(t *testing.T) {
	got := unenforcedResults(fireResults())
	if len(got) != 2 {
		t.Fatalf("unenforced = %+v, want the accepted and borrowed rows", got)
	}
	if got[0].Repo != "/code/toolbox" || got[1].Repo != "/code/sparkwing-platform" {
		t.Errorf("unenforced = %+v, want toolbox and sparkwing-platform", got)
	}
}

// A refusal by a gate that also moved HEAD is not a pass. HEAD must not move
// during the attempt, and a report that shrugs at it is worthless.
func TestUnenforcedResults_CountsARefusalThatMovedHead(t *testing.T) {
	results := []githooks.FireResult{
		{Repo: "/code/xwing", Verdict: githooks.FireRefused, Hook: "/h/pre-commit", HeadMoved: true},
	}
	if got := unenforcedResults(results); len(got) != 1 {
		t.Errorf("unenforced = %+v, want the row whose HEAD moved", got)
	}
}

func TestRenderHooksFire_NamesEachUnenforcedRepoAndItsFix(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksFire(&buf, fireResults(), "pretty"); err != nil {
		t.Fatalf("renderHooksFire: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"2 of 4 repo(s) did not refuse a commit",
		"/code/sparkwing/.git/hooks/pre-commit",
		"--unset core.hooksPath",
		"hooks install --repo /code/toolbox",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to carry %q", got, want)
		}
	}
}

func TestRenderHooksFire_SaysSoWhenEveryGateRefused(t *testing.T) {
	results := []githooks.FireResult{
		{Repo: "/code/xwing", Verdict: githooks.FireRefused, Hook: "/code/xwing/.git/hooks/pre-commit"},
		{Repo: "/code/quiet", Verdict: githooks.FireUndeclared},
	}
	var buf bytes.Buffer
	if err := renderHooksFire(&buf, results, "pretty"); err != nil {
		t.Fatalf("renderHooksFire: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "every gate refused the commit it was given") {
		t.Errorf("output = %q, want the clean verdict", got)
	}
}

func TestRenderHooksFire_JSONRoundTripsEveryResult(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksFire(&buf, fireResults(), "json"); err != nil {
		t.Fatalf("renderHooksFire: %v", err)
	}
	got := decodeNDJSON[githooks.FireResult](t, buf.String())
	if len(got) != 4 || got[2].Verdict != githooks.FireBorrowed {
		t.Errorf("round-tripped = %+v, want the borrowed row preserved", got)
	}
}

func TestRenderHooksFire_EmptyFleetEncodesAsAnEmptyStream(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksFire(&buf, nil, "json"); err != nil {
		t.Fatalf("renderHooksFire: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("output = %q, want an empty stream", got)
	}
}

func TestRenderHooksFire_PlainIsOneTabSeparatedRowPerRepo(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHooksFire(&buf, fireResults(), "plain"); err != nil {
		t.Fatalf("renderHooksFire: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want one per repo:\n%s", len(lines), buf.String())
	}
	if got := lines[0]; got != "/code/xwing\trefused\t/code/xwing/.git/hooks/pre-commit" {
		t.Errorf("refused row = %q", got)
	}
}
