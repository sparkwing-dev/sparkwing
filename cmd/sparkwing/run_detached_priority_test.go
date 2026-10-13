package main

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func TestRunDetached_PriorityRidesOnTheTriggerNotTheArgs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	e := newSubmitTestEnv(t)

	res := e.submit("--sw-priority", "front")
	trig, err := e.store().GetTrigger(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.TriggerEnv[orchestrator.SubmitPriorityKey]; got != "front" {
		t.Fatalf("trigger priority = %q, want front (unresolved until the consumer launches it)", got)
	}
	for k := range trig.Args {
		if strings.HasPrefix(k, "sw-") {
			t.Fatalf("--sw-priority leaked into the pipeline args as %q", k)
		}
	}
}

// safety: a key names one intent, and asking for the same work in more of a
// hurry is that same intent, so the repeat must answer with the original run.
func TestRunDetached_PriorityDoesNotDeduplicate(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	e := newSubmitTestEnv(t)

	first := e.submit("--sw-idempotency-key", "priority-dedup", "--sw-priority", "3")
	second := e.submit("--sw-idempotency-key", "priority-dedup", "--sw-priority", "back")
	if second.RunID != first.RunID {
		t.Fatalf("a differing priority started a second run %q, want the original %q",
			second.RunID, first.RunID)
	}
	if !second.AlreadySubmitted {
		t.Fatal("the repeat did not report already_submitted")
	}
}

func TestRunDetached_RefusesAnUnusablePriority(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	args := append(e.detachArgs("fixture", "--sw-output", "json"), "--sw-priority", "sideways")
	_, errOut, err := e.runStdout(args...)
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(errOut, "--sw-priority") {
		t.Fatalf("the refusal does not name the flag: %s", errOut)
	}
}

func TestDispatchRun_RefusesAnUnusablePriorityBeforeAnythingElse(t *testing.T) {
	err := dispatchRun([]string{"no-such-pipeline", "--sw-priority", "sideways"})
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(err.Error(), "--sw-priority") {
		t.Fatalf("dispatchRun = %v, want the flag named", err)
	}
}
