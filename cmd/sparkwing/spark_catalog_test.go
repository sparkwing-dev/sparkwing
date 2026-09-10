package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const catalogFixtureManifest = `{
  "name": "my-sparks",
  "description": "monorepo of pipeline libraries",
  "author": "tester",
  "version": "v1.2.3",
  "modules": [
    {"path": "docker", "module": "github.com/acme/my-sparks/docker", "description": "docker helpers"},
    {"path": "kube", "module": "github.com/acme/my-sparks/kube", "description": "kube helpers", "stability": "beta"}
  ]
}`

func TestSparksCatalogListsModulesFromALocalLibrary(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"spark.json": catalogFixtureManifest})

	var err error
	out := captureStdout(t, func() { err = runSparksCatalog([]string{"--path", dir, "-o", "pretty"}) })
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, want := range []string{"my-sparks", "docker", "kube", "docker helpers", "kube helpers", "beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "inflate --module") {
		t.Errorf("catalog output does not say how to inflate a block:\n%s", out)
	}
}

func TestSparksCatalogPlainFeedsInflate(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"spark.json": catalogFixtureManifest})

	var err error
	out := captureStdout(t, func() { err = runSparksCatalog([]string{"--path", dir, "-o", "plain"}) })
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	got := strings.Fields(out)
	if len(got) != 2 || got[0] != "docker" || got[1] != "kube" {
		t.Errorf("plain output = %q, want the two block names one per line", out)
	}
}

func TestSparksCatalogJSONIsNDJSON(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"spark.json": catalogFixtureManifest})

	var err error
	out := captureStdout(t, func() { err = runSparksCatalog([]string{"--path", dir, "-o", "json"}) })
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a summary line and two block lines, got %d:\n%s", len(lines), out)
	}
	var summary struct {
		Kind    string `json:"kind"`
		Library string `json:"library"`
		Blocks  int    `json:"blocks"`
	}
	if jerr := json.Unmarshal([]byte(lines[0]), &summary); jerr != nil {
		t.Fatalf("summary line: %v", jerr)
	}
	if summary.Kind != "summary" || summary.Library != "my-sparks" || summary.Blocks != 2 {
		t.Errorf("summary = %+v", summary)
	}
	var block struct {
		Name   string `json:"name"`
		Module string `json:"module"`
	}
	if jerr := json.Unmarshal([]byte(lines[1]), &block); jerr != nil {
		t.Fatalf("block line: %v", jerr)
	}
	if block.Name != "docker" || block.Module != "github.com/acme/my-sparks/docker" {
		t.Errorf("block = %+v", block)
	}
}

func TestSparksInflateWithoutModulePointsAtTheCatalog(t *testing.T) {
	err := runSparksInflate(nil)
	if err == nil {
		t.Fatal("expected --module to be required")
	}
	if !strings.Contains(err.Error(), "sparks catalog") {
		t.Errorf("error does not name the verb that lists the choices: %v", err)
	}
}
