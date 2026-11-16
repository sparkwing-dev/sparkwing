package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestNativeSubmissionWarnsAboutReservedLiteralNames(t *testing.T) {
	for _, route := range []string{"trigger", "cloud"} {
		t.Run(route, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			config := filepath.Join(t.TempDir(), "machine.yaml")
			t.Setenv("SPARKWING_CONFIG", config)
			if err := os.WriteFile(config, []byte("run:\n  submit_env_allow: [APPDATA, PSModulePath, BUILD_*, SPARKWING_*]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			const value = "private-requester-selector-3483"
			t.Setenv("APPDATA", value)
			t.Setenv("PSModulePath", "")
			if err := os.Unsetenv("PSModulePath"); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			var err error
			out := captureStderr(t, func() {
				if route == "trigger" {
					_, err = prepareNativeTrigger(context.Background(), triggerFlags{})
				} else {
					profile := "fixture"
					_, err = prepareNativeBurst(context.Background(), "fixture", runFlags{}, nil, userconfig.RunRouting{Profile: &profile}, client.EngineOptions{})
				}
			})
			if err == nil {
				t.Fatal("fixture unexpectedly prepared outside a repository")
			}
			for _, want := range []string{"warning:", "APPDATA", "PSModulePath", "job environment", "runner", "run.submit_env_allow"} {
				if !strings.Contains(out, want) {
					t.Errorf("warning lacks %q: %s", want, out)
				}
			}
			for _, forbidden := range []string{value, "BUILD_*", "SPARKWING_*"} {
				if strings.Contains(out, forbidden) {
					t.Errorf("warning exposes a value or warns about a wildcard: %s", out)
				}
			}
		})
	}
}
