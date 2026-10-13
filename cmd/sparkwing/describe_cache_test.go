//go:build unix

package main

import (
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
	writeExec(t, path, "#!/bin/sh\necho x >> "+counter+"\nread -r _ <&3\n"+
		`printf '{"reply":"1","ok":true,"result":{"protocol":"1","pipelines":`+pipelines+`}}\n' >&4`+"\n")
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
	protocolFixture(t, fixture, counter, `[{"name":"build","args":[{"name":"env","go_name":"Env","type":"string"}]}]`)
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
	protocolFixture(t, fixture, counter, `[{"name":"deploy"}]`)
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
		"legacy array":   `[{"name":"build"}]`,
		"other protocol": `{"protocol":"0","pipelines":[{"name":"build"}]}`,
		"no pipelines":   `{"protocol":"1"}`,
		"not json":       `{`,
	} {
		path := describeCachePath("k-" + strings.ReplaceAll(name, " ", "-"))
		writeRepoFile(t, path, body)
		if got := readDescribeFile(path); got != nil {
			t.Errorf("%s: readDescribeFile = %+v, want a miss", name, got)
		}
	}
}

func TestEnsureDescribeCacheRewritesALegacyEntry(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	sparkwingDir := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(sparkwingDir, "go.mod"), "module fixture\n\ngo 1.26\n")
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, describeCachePath(key), `[{"name":"build"}]`)
	fixture, counter := filepath.Join(t.TempDir(), "pipeline"), filepath.Join(t.TempDir(), "starts")
	protocolFixture(t, fixture, counter, `[{"name":"build"}]`)

	ensureDescribeCache(context.Background(), sparkwingDir, key, pipelineentry.Binary(fixture))
	ensureDescribeCache(context.Background(), sparkwingDir, key, pipelineentry.Binary(fixture))
	if got := readDescribeFile(describeCachePath(key)); len(got) != 1 || got[0].Name != "build" {
		t.Fatalf("cache after ensure = %+v", got)
	}
	if n := starts(t, counter); n != 1 {
		t.Fatalf("binary started %d times, want once", n)
	}
}

func TestSourceRunDescribesWithTheCallersBinding(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "source", ".sparkwing")
	caller := filepath.Join(t.TempDir(), "caller", ".sparkwing")
	writeRepoFile(t, filepath.Join(source, "go.mod"), "module fixture\n\ngo 1.26\n")
	writeRepoFile(t, filepath.Join(caller, "go.mod"), "module fixture\n\ngo 1.26\n")
	fixture := filepath.Join(t.TempDir(), "pipeline")
	// safety: the fixture names its one pipeline after the checkout it was
	// started in, standing in for the YAML binding that checkout declares.
	writeExec(t, fixture, "#!/bin/sh\nread -r _ <&3\n"+
		`printf '{"reply":"1","ok":true,"result":{"protocol":"1","pipelines":[{"name":"%s"}]}}\n' "$(basename "$PWD")" >&4`+"\n")
	key, err := bincache.PipelineCacheKey(source)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, describeCachePath(key), `{"protocol":"1","pipelines":[{"name":"source"}]}`)

	run := &pipelineRun{ctx: context.Background(), sparkwingDir: source, entry: pipelineentry.Binary(fixture)}
	if _, err := run.describedPipeline("source"); err != nil {
		t.Fatalf("an in-place run: %v", err)
	}
	run.opts.ExecutionDir = caller
	if _, err := run.describedPipeline("caller"); err != nil {
		t.Fatalf("a source run resolved against the source binding: %v", err)
	}
	if _, err := run.describedPipeline("source"); err == nil {
		t.Fatal("a source run accepted a name only the source binds")
	}
}
