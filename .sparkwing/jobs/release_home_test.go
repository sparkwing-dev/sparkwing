package jobs

import (
	"strings"
	"testing"
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
	for _, n := range plan.Jobs {
		if id := n.ID; id != "require-isolated-home" && !ancestors(t, plan, id)["require-isolated-home"] {
			t.Errorf("%s runs without waiting on require-isolated-home, so a release in the operational home reaches it", id)
		}
	}
	if !mustNode(t, plan, "require-isolated-home").Inline {
		t.Error("require-isolated-home must run inline, ahead of any dispatched job")
	}
}

func TestReleaseHomeGuardFailsItsJobAndRunsUnderDryRun(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")
	host, plan, doc := prepareWork(t, &requireIsolatedHomeJob{})
	if len(doc.Steps) != 1 {
		t.Fatalf("home guard steps = %+v", doc.Steps)
	}
	step := doc.Steps[0]
	if !step.SafeWithoutDryRun {
		t.Errorf("step %s skips under dry run", step.ID)
	}
	if _, err := host.RunStep(t.Context(), plan, "job", step.ID); err == nil || !strings.Contains(err.Error(), "SPARKWING_HOME") {
		t.Fatalf("home guard = %v", err)
	}
}

func TestReleaseDryRunRunsTheReadOnlyCutChecks(t *testing.T) {
	_, _, doc := prepareWork(t, &releaseCutChecksJob{})
	safe := map[string]bool{}
	for _, step := range doc.Steps {
		safe[step.ID] = step.SafeWithoutDryRun
	}
	for _, id := range []string{"version-freshness", "go-directives", "sdk-pins", "changelog-links"} {
		if !safe[id] {
			t.Errorf("release-cut-checks step %s skips under --sw-dry-run, so a preview passes a cut it refuses", id)
		}
	}
}
