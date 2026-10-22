//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func protocolFixture(t *testing.T, path, counter, pipelines string) {
	t.Helper()
	writeExec(t, path, `#!/bin/sh
echo x >> `+counter+`
while read -r line <&3; do
 id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
 case "$line" in
 *'"op":"hello"'*) payload='"ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"fixture"},"features":[]}}' ;;
 *'"op":"describe"'*) payload='"ok":true,"result":{"pipelines":`+pipelines+`}}' ;;
 *) exit 9 ;;
 esac
 printf '{"reply":"%s",%s\n' "$id" "$payload" >&4
done
`)
}

func starts(t *testing.T, counter string) int {
	t.Helper()
	raw, err := os.ReadFile(counter)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "x")
}

// hack: publishing the fixture as the key's bincache entry stands in for a
// compile, which the cache test does not need.
func materializeFixture(t *testing.T, sparkwingDir, fixture string) string {
	t.Helper()
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		body, err := os.ReadFile(fixture)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestDescribeCacheReadsTheProtocolDocumentOnceAndInvalidatesOnSourceChange(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	sparkwingDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(sparkwingDir, "go.mod"), "module fixture\n\ngo 1.26\n")
	writeRepoFile(t, filepath.Join(sparkwingDir, "main.go"), "package main\n")
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	protocolFixture(t, fixture, counter, `[{"name":"build","args":[{"name":"env","type":"string","required":false}]}]`)
	firstKey := materializeFixture(t, sparkwingDir, fixture)

	for range 2 {
		got, err := readDescribeCache(context.Background(), sparkwingDir)
		if err != nil || len(got) != 1 || got[0].Name != "build" || len(got[0].Args) != 1 {
			t.Fatalf("readDescribeCache = %+v, %v", got, err)
		}
	}
	if n := starts(t, counter); n != 1 {
		t.Fatalf("binary started %d times, want once with the second read served from the cache", n)
	}

	writeRepoFile(t, filepath.Join(sparkwingDir, "main.go"), "package main\n\n// changed\n")
	protocolFixture(t, fixture, counter, `[{"name":"deploy","args":[]}]`)
	if key := materializeFixture(t, sparkwingDir, fixture); key == firstKey {
		t.Fatal("a source change kept the cache key")
	}
	got, err := readDescribeCache(context.Background(), sparkwingDir)
	if err != nil || len(got) != 1 || got[0].Name != "deploy" {
		t.Fatalf("after a source change readDescribeCache = %+v, %v", got, err)
	}
	if n := starts(t, counter); n != 2 {
		t.Fatalf("binary started %d times, want a fresh describe for the new key", n)
	}
}

func TestDescribeCacheIgnoresDocumentsOfAnotherShape(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	for name, body := range map[string]string{
		"legacy array": `[{"name":"build","args":[]}]`,
		"missing args": `{"pipelines":[{"name":"build"}]}`,
		"no pipelines": `{}`,
		"not json":     `{`,
	} {
		path := describeCachePath("k-" + strings.ReplaceAll(name, " ", "-"))
		writeRepoFile(t, path, body)
		if got := readDescribeFile(path); got != nil {
			t.Errorf("%s: readDescribeFile = %+v, want a miss", name, got)
		}
	}
}

func TestDescribeSeparatesExecutionGrammarFromPublicMetadata(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	sparkwingDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(sparkwingDir, "go.mod"), "module fixture\n\ngo 1.26\n")
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	protocolFixture(t, fixture, counter, `[{"name":"deploy","args":[{"name":"token","type":"string","required":false,"secret":true,"default":"deploy"},{"name":"other","type":"string","required":false,"secret":true,"default":"bool"},{"name":"enabled","type":"bool","required":false}]}]`)
	gate, err := describeEntry(t.Context(), sparkwingDir, pipelineentry.Binary(fixture))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := findPipelineSchema(gate.Declarations().Pipelines, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	args, err := parsePipelineArgs(schema, []string{"--enabled"})
	if err != nil || args["enabled"] != "true" {
		t.Fatalf("redaction changed argument grammar: %v %v", args, err)
	}
	if err := storeDescribe(sparkwingDir, "public", gate.Presentation()); err != nil {
		t.Fatal(err)
	}
	cached := readDescribeFile(describeCachePath("public"))
	if len(cached) != 1 || cached[0].Name != "***" || cached[0].Args[2].Type != "***" {
		t.Fatalf("private routing metadata entered public cache: %+v", cached)
	}
	var help bytes.Buffer
	printPipelineHelp(&help, cached[0])
	if strings.Contains(help.String(), "deploy") || strings.Contains(help.String(), "bool") {
		t.Fatalf("private declarations entered help: %s", &help)
	}
}

func TestIncompleteDiscoveryCacheWithholdsUnknownNamesAndRoutingHints(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	sparkwingDir := filepath.Join(t.TempDir(), ".sparkwing")
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	protocolFixture(t, fixture, counter, `[{"name":"unknown-private-name","short":"unknown-private-short","args":[{"name":"unknown-private-arg","short":"unknown-private-alias","type":"string","required":false,"job_id":"unknown-private-job"}]},{"name":"broken","args":[],"error":{"code":"plan_failed","message":"unknown-private-error"}}]`)
	if err := writeDescribeCache(t.Context(), sparkwingDir, "incomplete", pipelineentry.Binary(fixture)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{describeCachePath("incomplete"), byRepoDescribePath(sparkwingDir)} {
		raw, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(raw), "unknown-private") || strings.Contains(string(raw), "broken") {
			t.Fatalf("unclassified presentation cached: %s %v", raw, err)
		}
		if !strings.Contains(string(raw), "details withheld") {
			t.Fatalf("incomplete presentation lacks explanation: %s", raw)
		}
	}
}
