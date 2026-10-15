package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestSparksCommandsUseDeclaredSelectorAndPreserveProject(t *testing.T) {
	for _, format := range []string{"pretty", "json", "plain"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			root := writeSparkFixture(t, map[string]string{
				".sparkwing/sparkwing.yaml": "pipelines:\n  - name: ci\n    entrypoint: CI\n",
			})
			for _, action := range []string{"add", "remove"} {
				args := []string{"-C", root, "pipeline", "sparks", action, "--spark", "chosen", "-o", format}
				if action == "add" {
					args = append(args, "--source", "example.com/library", "--version", "v1.2.3")
				}
				var commandErr error
				output := captureStdout(t, func() { commandErr = runSparkwing(args) })
				if commandErr != nil || strings.TrimSpace(output) == "" {
					t.Fatalf("%s: %v, output=%q", action, commandErr, output)
				}
				if format == "json" {
					var result struct {
						Spark  string `json:"spark"`
						Source string `json:"source"`
					}
					if err := json.Unmarshal([]byte(output), &result); err != nil || result.Spark != "chosen" || result.Source != "example.com/library" {
						t.Fatalf("%s receipt: %v, output=%s", action, err, output)
					}
				} else if format == "plain" && output != "chosen\n" {
					t.Fatalf("%s plain receipt: %q", action, output)
				}
				cfg, err := projectconfig.Load(filepath.Join(root, ".sparkwing", projectconfig.Filename))
				if err != nil || len(cfg.Pipelines) != 1 || cfg.Pipelines[0].Name != "ci" {
					t.Fatalf("%s changed pipeline registry: %+v, %v", action, cfg, err)
				}
				if action == "add" {
					if len(cfg.Sparks) != 1 || cfg.Sparks[0].Name != "chosen" || cfg.Sparks[0].Version != "v1.2.3" {
						t.Fatalf("added spark: %+v", cfg.Sparks)
					}
				} else if len(cfg.Sparks) != 0 {
					t.Fatalf("removed spark remains: %+v", cfg.Sparks)
				}
			}
		})
	}
}

func TestSparksCommandsRefuseInvalidSelectorsWithoutWriting(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	root := writeSparkFixture(t, map[string]string{
		".sparkwing/sparkwing.yaml": "pipelines: []\nsparks:\n  - name: existing\n    source: example.com/library\n    version: v1.2.3\n",
	})
	path := filepath.Join(root, ".sparkwing", "sparkwing.yaml")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "--source", "example.com/library", "--spark", "existing"},
		{"remove", "--spark", "missing"},
		{"remove", "--name", "existing"},
		{"update", "--name", "existing"},
	} {
		var commandErr error
		output := captureStdout(t, func() {
			commandErr = runSparkwing(append([]string{"-C", root, "pipeline", "sparks"}, args...))
		})
		got, readErr := os.ReadFile(path)
		if commandErr == nil || output != "" || readErr != nil || string(got) != string(want) {
			t.Errorf("%v: %v, output=%q, read=%v", args, commandErr, output, readErr)
		}
	}
}
