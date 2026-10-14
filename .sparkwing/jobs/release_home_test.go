package jobs

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func operatorHome() (string, error) { return "/home/operator", nil }

func TestReleaseRequiresIsolatedSparkwingHome(t *testing.T) {
	err := requireReleaseHome(func(string) string { return "" }, operatorHome)
	if err == nil {
		t.Fatal("release accepted the default shared Sparkwing home")
	}
	if !strings.Contains(err.Error(), "SPARKWING_HOME") || !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("error = %q, want actionable isolated-home guidance", err)
	}
}

func TestReleaseAcceptsExplicitSparkwingHome(t *testing.T) {
	err := requireReleaseHome(func(key string) string {
		if key == "SPARKWING_HOME" {
			return "/tmp/sparkwing-release"
		}
		return ""
	}, operatorHome)
	if err != nil {
		t.Fatalf("release with isolated home: %v", err)
	}
}

func TestReleaseRejectsExplicitDefaultSparkwingHome(t *testing.T) {
	err := requireReleaseHome(func(string) string { return "/home/operator/.sparkwing/" }, operatorHome)
	if err == nil {
		t.Fatal("release accepted the operational default store through an explicit environment value")
	}
}

func TestReleaseRefusesTheOperationalHomeBeforeAnyOtherJob(t *testing.T) {
	plan := releasePlan(t)
	for _, n := range plan.Nodes() {
		if id := n.ID(); id != "require-isolated-home" && !ancestors(t, plan, id)["require-isolated-home"] {
			t.Errorf("%s runs without waiting on require-isolated-home, so a release in the operational home reaches it", id)
		}
	}
	if !mustNode(t, plan, "require-isolated-home").IsInline() {
		t.Error("require-isolated-home must run inline, ahead of any dispatched job")
	}
}

func TestReleaseHomeGuardFailsItsJobAndRunsUnderDryRun(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")
	w := sparkwing.NewWork()
	if _, err := (&requireIsolatedHomeJob{}).Work(w); err != nil {
		t.Fatal(err)
	}
	for _, st := range w.Steps() {
		if !st.IsSafeWithoutDryRun() {
			t.Errorf("step %s skips under --sw-dry-run, so a preview in the operational home is not refused", st.ID())
		}
	}
	if _, err := sparkwing.RunWork(t.Context(), w); err == nil || !strings.Contains(err.Error(), "SPARKWING_HOME") {
		t.Fatalf("guard job with SPARKWING_HOME unset = %v, want a refusal naming SPARKWING_HOME", err)
	}
}
