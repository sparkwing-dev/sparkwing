package jobs

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestGateRunsTheEndToEndTier(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&Gate{}).Work(w); err != nil {
		t.Fatal(err)
	}
	if w.StepByID("e2e") == nil {
		t.Fatal("the gate does not run the end-to-end tier, so every test behind the e2e tag runs nowhere")
	}
	if !stepWaitsOn(w, "e2e", "build") {
		t.Error("e2e does not wait on build")
	}
	if stepWaitsOn(w, "e2e", "test") {
		t.Error("e2e waits on test, so the two tiers serialize instead of running beside each other")
	}
}

func TestEveryTierJudgingTestsReadsTheEndToEndTag(t *testing.T) {
	for name, command := range map[string]string{
		"e2e":          e2eGoCommand(14),
		"vet":          vetGoCommand(14),
		"race-touched": raceGoCommand(14, []string{"./x"}),
		"lint":         lintCommandFor(true),
	} {
		if !strings.Contains(command, "e2e") {
			t.Errorf("%s does not read the e2e tag, so the tier it cannot see is judged by nothing: %q", name, command)
		}
	}
}
