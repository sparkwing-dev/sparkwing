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
	want := "edit one: sparkwing pipeline sparks inflate --module github.com/acme/my-sparks/docker"
	if !strings.Contains(out, want) {
		t.Errorf("catalog must name the module inflate resolves, not a bare path:\n%s", out)
	}
}

func TestSparksCatalogDoesNotOfferPackagesToInflate(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"spark.json": `{
  "name": "one-module-lib",
  "description": "a single Go module with several packages",
  "author": "tester",
  "packages": [
    {"path": "docker", "description": "docker helpers"},
    {"path": "kube", "description": "kube helpers"}
  ]
}`})

	var err error
	out := captureStdout(t, func() { err = runSparksCatalog([]string{"--path", dir, "-o", "pretty"}) })
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if strings.Contains(out, "inflate --module docker") || strings.Contains(out, "inflate --module kube") {
		t.Errorf("a packages[] entry is not a module, so it cannot be inflated by name:\n%s", out)
	}
	if !strings.Contains(out, "one Go module") {
		t.Errorf("catalog does not say these rows are packages inside one module:\n%s", out)
	}
}

func TestSparksCatalogErrorsCarryTheCatalogVerb(t *testing.T) {
	dir := t.TempDir()

	err := runSparksCatalog([]string{"--path", dir})
	if err == nil {
		t.Fatal("expected a directory with no spark.json to fail")
	}
	if !strings.Contains(err.Error(), "spark catalog") {
		t.Errorf("error names the wrong verb: %v", err)
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
	want := []string{"github.com/acme/my-sparks/docker", "github.com/acme/my-sparks/kube"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("plain output = %q, want %q -- the values `inflate --module` resolves", out, want)
	}
}

func TestSparksCatalogNamesAPackagesLibraryByItsGoModule(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{
		"spark.json": `{
  "name": "one-module-lib",
  "description": "a single Go module with several packages",
  "author": "tester",
  "packages": [{"path": "docker", "description": "docker helpers"}]
}`,
		"go.mod": "module github.com/acme/one-module-lib\n\ngo 1.26.0\n",
	})

	var err error
	out := captureStdout(t, func() { err = runSparksCatalog([]string{"--path", dir, "-o", "pretty"}) })
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !strings.Contains(out, "inflate --module github.com/acme/one-module-lib") {
		t.Errorf("catalog does not name the library's own module:\n%s", out)
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
