package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparks"
)

func TestFinalSchema(t *testing.T) {
	raw := `defaults:
  args: {}
  guards: {}
  requires: []
pipelines:
  - name: build
    entrypoint: Build
    profile: prod
    hidden: false
    args: {target: release}
    on:
      push: {branches: [main], paths: ["src/**"]}
      pull_request: {actions: [opened], branches: [main]}
      pre_commit: {}
      schedule:
        - cron: "0 * * * *"
          where: controller
sparks: [{name: core, source: github.com/acme/core, version: v1.0.0}]
`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Args == nil || cfg.Defaults.Requires == nil || cfg.Defaults.Guards == nil || cfg.Pipelines[0].Hidden == nil || *cfg.Pipelines[0].Hidden {
		t.Fatalf("presence lost: %+v", cfg)
	}
	if cfg.Pipelines[0].On.Schedule[0].TZ != nil {
		t.Fatal("parser filled defaults")
	}
}

func TestRejectMalformedProject(t *testing.T) {
	cases := []string{
		"profiles: {}", "defaults: null", "defaults: {args: {x: a, x: b}}", "{}\n---\n{}", "pipelines: nope",
		"defaults: {guards: {when: [profile:local]}}", "defaults: {guards: {when: [git:branch=]}}", "defaults: {guards: {when: [arg:=v]}}",
		"pipelines: [{name: x, entrypoint: X, on: {webhook: {}}}]",
		"pipelines: [{name: x, entrypoint: X, on: {pre_commit: {x: y}}}]",
		"pipelines: [{name: x, entrypoint: X, on: {push: null}}]",
		"pipelines: [{name: x, entrypoint: X}, {name: x, entrypoint: Y}]",
		"pipelines: [{name: x}]", "pipelines: [{name: '../x', entrypoint: X}]",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse([]byte(raw))
			if err == nil {
				t.Fatal("accepted invalid YAML")
			}
			if !strings.Contains(err.Error(), "line") {
				t.Fatalf("missing line: %v", err)
			}
		})
	}
}

func TestScheduleValidation(t *testing.T) {
	for _, fragment := range []string{"where: nowhere", "where: local, catch_up: 25h", "where: local, catch_up: 1m", "where: local, overlap: replace", "where: local, tz: Not/AZone", "where: local, name: Upper"} {
		_, err := Parse([]byte("pipelines: [{name: build, entrypoint: Build, on: {schedule: [{cron: '0 * * * *', " + fragment + "}]}}]"))
		if err == nil {
			t.Errorf("accepted %s", fragment)
		}
	}
	for _, catchUp := range []string{"2m", "24h"} {
		if _, err := Parse([]byte("pipelines: [{name: build, entrypoint: Build, on: {schedule: [{cron: '0 * * * *', where: local, catch_up: " + catchUp + "}]}}]")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadAndDiscover(t *testing.T) {
	root := t.TempDir()
	missing, err := Load(filepath.Join(root, "absent"))
	if err != nil || missing != nil {
		t.Fatalf("missing: %v %v", missing, err)
	}
	dir := filepath.Join(root, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte("defaults: {args: {x: y}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "sub")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	got, cfg, err := Discover(child)
	if err != nil || got != path || cfg.Defaults.Args["x"] != "y" {
		t.Fatalf("discover: %q %v %v", got, cfg, err)
	}
}

func TestSchedulesRequireUniqueNamesAndFiveFields(t *testing.T) {
	for _, entries := range []string{
		"[{cron: '0 * * * *', where: local}, {cron: '0 * * * *', where: controller}]",
		"[{name: daily, cron: '0 * * * *', where: local}, {name: daily, cron: '0 * * * *', where: controller}]",
		"[{cron: '61 * * * *', where: local}]",
		"[{cron: '* * * * * *', where: local}]",
		"[{cron: '0 * * * *', where: local, args: {Bad_Flag: x}}]",
	} {
		if _, err := Parse([]byte("pipelines: [{name: build, entrypoint: Build, on: {schedule: " + entries + "}}]")); err == nil {
			t.Errorf("accepted %s", entries)
		}
	}
}

func TestGuardsPreserveReplacementAndMergeInputs(t *testing.T) {
	cfg, err := Parse([]byte("defaults: {guards: &guards {when: [where:local, 'arg:target=release', 'git:branch=default']}}\npipelines: [{name: a, entrypoint: A, guards: {}}, {name: b, entrypoint: B, guards: {<<: *guards}}]"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pipelines[0].Guards == nil || len(cfg.Pipelines[0].Guards.When) != 0 || len(cfg.Pipelines[1].Guards.When) != 3 {
		t.Fatal("guard replacement/merge lost")
	}
}

func TestSparksWriteKeepsCommentsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	if err := os.WriteFile(path, []byte("# libraries\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteSparksSection(path, []sparks.Library{{Name: "core", Source: "github.com/acme/core", Version: "v1.0.0"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "# libraries") {
		t.Fatalf("lost comment: %s %v", raw, err)
	}
	manifest, err := LoadSparksManifest(filepath.Dir(path))
	if err != nil || len(manifest.Libraries) != 1 {
		t.Fatalf("manifest: %+v %v", manifest, err)
	}
	before := string(raw)
	if err := WriteSparksSection(path, []sparks.Library{{Name: "bad"}}); err == nil {
		t.Fatal("wrote invalid spark")
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != before {
		t.Fatal("invalid edit changed file")
	}
}
