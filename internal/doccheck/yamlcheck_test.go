package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestYAMLExamplesUseProjectSchema(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"final", "defaults: {guards: {when: [where:local]}}\npipelines:\n- name: build\n  entrypoint: Build\n  on: {schedule: [{cron: '@daily', where: local}]}\n", true},
		{"connections", "profiles: {}\npipelines:\n- name: build\n  entrypoint: Build\n", false},
		{"mapping_schedule", "pipelines:\n- name: build\n  entrypoint: Build\n  on: {schedule: {cron: '@daily', where: local}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "example.md"), []byte("```yaml\n"+tc.body+"```\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := checkYAMLConfigs(dir); got != tc.valid {
				t.Fatalf("valid = %v, want %v", got, tc.valid)
			}
		})
	}
}
