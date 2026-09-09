package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestYAMLExamplesMatchScheduleValidation(t *testing.T) {
	cases := []struct {
		name     string
		schedule string
		valid    bool
	}{
		{name: "scalar", schedule: `"0 3 * * *"`},
		{name: "missing location", schedule: "\n        cron: '@daily'\n        tz: UTC\n        overlap: queue"},
		{name: "explicit location", schedule: "\n        cron: '@daily'\n        where: local\n        tz: UTC\n        overlap: queue", valid: true},
		{name: "named entry", schedule: "\n        - name: morning\n          cron: '@daily'\n          where: controller", valid: true},
		{name: "unknown schedule field", schedule: "\n        - name: morning\n          cron: '@daily'\n          where: local\n          unexpected: value"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := "pipelines:\n  - name: example\n    entrypoint: Example\n    on:\n      schedule: " + testCase.schedule + "\n"
			configPath := filepath.Join(t.TempDir(), "sparkwing.yaml")
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := projectconfig.Load(configPath)
			if (err == nil) != testCase.valid {
				t.Fatalf("production loader error = %v, want valid=%v", err, testCase.valid)
			}
			docs := writeYAMLExample(t, body)
			if got := checkYAMLConfigs(docs); got != testCase.valid {
				t.Fatalf("documentation acceptance = %v, want %v", got, testCase.valid)
			}
		})
	}
}

func TestYAMLExamplesRejectUnknownPipelineField(t *testing.T) {
	docs := writeYAMLExample(t, "pipelines:\n  - name: example\n    entrypoint: Example\n    unexpected: value\n")
	if checkYAMLConfigs(docs) {
		t.Fatal("documentation checker accepted an unknown pipeline field")
	}
}

func TestProfileExamplesKeepStrictFields(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "valid"
		field := "url"
		if !valid {
			name = "unknown controller field"
			field = "unexpected"
		}
		t.Run(name, func(t *testing.T) {
			docs := writeYAMLExample(t, "profiles:\n  example:\n    controller:\n      "+field+": https://example.invalid\n")
			if got := checkProfileConfigs(docs); got != valid {
				t.Fatalf("profile documentation acceptance = %v, want %v", got, valid)
			}
		})
	}
}

func writeYAMLExample(t *testing.T, body string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "example.md"), []byte("```yaml\n"+body+"```\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}
