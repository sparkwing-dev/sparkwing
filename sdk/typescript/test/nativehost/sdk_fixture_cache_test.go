package nativehost_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSDKFixtureBuildsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the counting compiler fixture uses a Unix shell")
	}
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			tools := t.TempDir()
			count := filepath.Join(tools, "count")
			node := filepath.Join(tools, "node")
			if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf 'build\\n' >> \"$SDK_FIXTURE_COUNT\"\nif [ \"$SDK_FIXTURE_OUTCOME\" = failure ]; then echo compiler-probe-failed >&2; exit 1; fi\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools)
			t.Setenv("SDK_FIXTURE_COUNT", count)
			t.Setenv("SDK_FIXTURE_OUTCOME", outcome)
			var cache sdkBuild
			for range 2 {
				fixture, err := cache.load(t.Context())
				if outcome == "failure" {
					if err == nil || !strings.Contains(err.Error(), "compiler-probe-failed") {
						t.Fatalf("compiler failure = %v", err)
					}
				} else if err != nil || fixture.node != node || fixture.root == "" {
					t.Fatalf("compiled fixture = %+v, %v", fixture, err)
				}
			}
			calls, err := os.ReadFile(count)
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != "build\n" {
				t.Fatalf("compiler invocations = %q, want one build", calls)
			}
		})
	}
}
