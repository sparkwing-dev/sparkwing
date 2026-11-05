//go:build unix

package nodehost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func historicalStorageLifecycle(t *testing.T, ctx context.Context, session *executorSession, task, lifecycle nodeproto.JobSpec) {
	t.Helper()
	host, input, spec, _, _, backend := executorMemoFixture(t, nil)
	input.NodeID = task.ID
	input.Storage.Authority.Attempt.Value.NodeID = task.ID
	spec.Request.Request.Pipeline = "compat"
	hit := backend.hit
	backend.hit.Found = false
	missHash, result, err := host.memo(ctx, session, input, task, spec)
	if err != nil || missHash == "" || result != nil || backend.lookup == nil || backend.copy != nil {
		t.Fatalf("historical memo miss: hash=%s result=%+v lookup=%+v copy=%+v error=%v", missHash, result, backend.lookup, backend.copy, err)
	}
	wantHash, err := memoValueKey("compat-memo")
	if err != nil {
		t.Fatal(err)
	}
	if missHash != runspec.Hash(wantHash) {
		t.Fatalf("historical memo callback value: hash=%s want=%s", missHash, wantHash)
	}
	output := historicalRun(t, ctx, session, "output")
	if output.Outcome != "success" || len(output.Output) == 0 {
		t.Fatalf("memo source output: %+v", output)
	}
	hit.Output.Value = outputDigest(output.Output)
	backend.values[hit.Output.Value] = output.Output
	backend.hit = hit
	hitHash, result, err := host.memo(ctx, session, input, task, spec)
	if err != nil || hitHash != missHash || result == nil || result.Outcome != runstate.NodeMemoized || result.OutputRecord == nil || backend.copy == nil {
		t.Fatalf("historical memo hit: hash=%s result=%+v copy=%+v error=%v", hitHash, result, backend.copy, err)
	}
	if backend.lookup.Key.Job != task.ID || backend.lookup.Key.Pipeline != "compat" || backend.copy.Destination != input.Storage.Authority.Attempt.Value {
		t.Fatal("historical memo restore changed job or awarded attempt")
	}
	if len(backend.readers) == 0 {
		t.Fatal("historical memo hit did not verify stored output")
	}
	for _, reader := range backend.readers {
		if !reader.closed || !reader.Verified() {
			t.Fatal("historical memo restore left output unread or unverified")
		}
	}
	historicalDirectoryCache(t, ctx, lifecycle)
}

func historicalDirectoryCache(t *testing.T, ctx context.Context, job nodeproto.JobSpec) {
	t.Helper()
	if len(job.Caches) != 1 {
		t.Fatalf("historical cache declarations: %+v", job.Caches)
	}
	declaration := job.Caches[0]
	if declaration.Name != "compat-cache" || declaration.Path != "compat-cache" || declaration.KeyScope != "compat-cache" || len(declaration.KeyFiles) != 1 || declaration.KeyFiles[0] != "compat-cache-key.txt" || declaration.Resolver != "" {
		t.Fatalf("historical cache declaration changed: %+v", declaration)
	}
	input, adapter, backend, _ := depcacheFixture(t)
	writePublishedFile(t, input.Entry.Directory, declaration.KeyFiles[0], "compat-cache-v1\n", 0o600)
	adapter.Warn = func(message string) { t.Errorf("historical cache warning: %s", message) }
	finish, err := adapter.Prepare(ctx, input, job)
	if err != nil || finish == nil || backend.lookups != 1 {
		t.Fatalf("historical cache miss: finalizer=%t lookups=%d error=%v", finish != nil, backend.lookups, err)
	}
	writePublishedFile(t, input.Entry.Directory, filepath.Join(declaration.Path, "dependency.txt"), "local fake dependency", 0o600)
	result := runstate.NodeResult{Outcome: runstate.NodeSuccess}
	if err := finish(ctx, input, &result); err != nil || backend.commit == nil || len(backend.puts) != 1 {
		t.Fatalf("historical cache save: commit=%+v puts=%d error=%v", backend.commit, len(backend.puts), err)
	}
	if err := os.RemoveAll(filepath.Join(input.Entry.Directory, declaration.Path)); err != nil {
		t.Fatal(err)
	}
	finish, err = adapter.Prepare(ctx, input, job)
	if err != nil || finish == nil || backend.lookups != 2 {
		t.Fatalf("historical cache restore: finalizer=%t lookups=%d error=%v", finish != nil, backend.lookups, err)
	}
	value, err := os.ReadFile(filepath.Join(input.Entry.Directory, declaration.Path, "dependency.txt"))
	if err != nil || string(value) != "local fake dependency" {
		t.Fatalf("historical cache bytes: %q error=%v", value, err)
	}
	puts := len(backend.puts)
	if err := finish(ctx, input, &result); err != nil || len(backend.puts) != puts {
		t.Fatalf("historical cache hit saved again: puts=%d want=%d error=%v", len(backend.puts), puts, err)
	}
}
