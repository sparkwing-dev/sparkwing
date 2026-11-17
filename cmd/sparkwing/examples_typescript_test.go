package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var typeScriptExampleNames = []string{"lint-test-node-ts", "test-matrix-artifacts-deploy-ts", "secret-and-cache-ts"}

func TestExamplesListTypeScriptBesideGoWithTheirLanguage(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	var commandErr error
	out := captureStdout(t, func() { commandErr = runSparkwing([]string{"examples", "-o", "json"}) })
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	languages := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var record templateListRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		languages[record.Name] = record.Language
	}
	for _, name := range typeScriptExampleNames {
		if languages[name] != "typescript" {
			t.Errorf("%s language = %q, want typescript", name, languages[name])
		}
	}
	if languages["lint-test-go"] != "go" {
		t.Errorf("lint-test-go language = %q, want go", languages["lint-test-go"])
	}

	out = captureStdout(t, func() {
		commandErr = runSparkwing([]string{"examples", "--language", "typescript", "-o", "plain"})
	})
	if commandErr != nil || !slices.Equal(strings.Split(strings.TrimSpace(out), "\n"), typeScriptExampleNames) {
		t.Fatalf("examples --language typescript: %v\n%s", commandErr, out)
	}
	if err := runSparkwing([]string{"examples", "--language", "python", "-o", "plain"}); err == nil || !strings.Contains(err.Error(), "go, typescript") {
		t.Fatalf("an unknown language was not refused with the choices: %v", err)
	}
}

func TestTypeScriptExampleBodyIsTheCheckedSource(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	for _, name := range typeScriptExampleNames {
		source, err := os.ReadFile(filepath.Join("..", "..", "sdk", "typescript", "examples", name, "pipeline.ts"))
		if err != nil {
			t.Fatal(err)
		}
		var commandErr error
		plain := captureStdout(t, func() {
			commandErr = runSparkwing([]string{"examples", "--example", name, "--body", "-o", "plain"})
		})
		if commandErr != nil || strings.TrimSpace(plain) != strings.TrimSpace(string(source)) {
			t.Fatalf("%s plain body differs from sdk/typescript/examples: %v", name, commandErr)
		}
		raw := captureStdout(t, func() {
			commandErr = runSparkwing([]string{"examples", "--example", name, "--body", "-o", "json"})
		})
		var detail templateDetailJSON
		if err := json.Unmarshal([]byte(raw), &detail); commandErr != nil || err != nil || detail.Language != "typescript" || detail.RenderedBody != string(source) || detail.ReadMe == "" {
			t.Fatalf("%s json detail: %v %v %s", name, commandErr, err, raw)
		}
		pretty := captureStdout(t, func() {
			commandErr = runSparkwing([]string{"examples", "--example", name, "-o", "pretty"})
		})
		if commandErr != nil || !strings.Contains(pretty, "typescript") {
			t.Fatalf("%s pretty detail does not name its language: %v\n%s", name, commandErr, pretty)
		}
	}
}

func TestDocsSearchFindsTypeScriptExamples(t *testing.T) {
	var names []string
	for _, hit := range searchExamples("typescript matrix") {
		names = append(names, hit.Name)
	}
	if !slices.Contains(names, "test-matrix-artifacts-deploy-ts") {
		t.Fatalf("docs search for a TypeScript matrix found %v", names)
	}
}
