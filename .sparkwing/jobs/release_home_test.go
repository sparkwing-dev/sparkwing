package jobs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestReleaseRefusesOperationalHomeDuringPlanning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, store := range []string{"", filepath.Join(home, ".sparkwing")} {
		t.Run(store, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", store)
			_, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Release", nil)
			if err == nil {
				t.Fatalf("release planning = %v, want isolated-home refusal before intake", err)
			}
			if _, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Build", nil); err != nil {
				t.Fatalf("another pipeline in the operational home: %v", err)
			}
		})
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if _, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Release", nil); err != nil {
		t.Fatalf("release planning with isolated home: %v", err)
	}
}

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
