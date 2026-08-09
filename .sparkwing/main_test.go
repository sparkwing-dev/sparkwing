package main

import (
	"strings"
	"testing"
)

func TestReleaseRequiresIsolatedSparkwingHome(t *testing.T) {
	err := requireReleaseHome([]string{"sparkwing-pipelines", "release"}, func(string) string { return "" }, func() (string, error) { return "/home/operator", nil })
	if err == nil {
		t.Fatal("release accepted the default shared Sparkwing home")
	}
	if !strings.Contains(err.Error(), "SPARKWING_HOME") || !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("error = %q, want actionable isolated-home guidance", err)
	}
}

func TestReleaseAcceptsExplicitSparkwingHome(t *testing.T) {
	err := requireReleaseHome([]string{"sparkwing-pipelines", "release"}, func(key string) string {
		if key == "SPARKWING_HOME" {
			return "/tmp/sparkwing-release"
		}
		return ""
	}, func() (string, error) { return "/home/operator", nil })
	if err != nil {
		t.Fatalf("release with isolated home: %v", err)
	}
}

func TestOtherPipelinesDoNotRequireExplicitSparkwingHome(t *testing.T) {
	if err := requireReleaseHome([]string{"sparkwing-pipelines", "pre-push"}, func(string) string { return "" }, func() (string, error) { return "/home/operator", nil }); err != nil {
		t.Fatalf("pre-push: %v", err)
	}
}

func TestReleaseRejectsExplicitDefaultSparkwingHome(t *testing.T) {
	err := requireReleaseHome([]string{"sparkwing-pipelines", "release"}, func(string) string {
		return "/home/operator/.sparkwing"
	}, func() (string, error) { return "/home/operator", nil })
	if err == nil {
		t.Fatal("release accepted the operational default store through an explicit environment value")
	}
}
