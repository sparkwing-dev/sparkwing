package fs_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestLogStore_RejectsPathEscapingIDs(t *testing.T) {
	root := t.TempDir()
	ls, err := fs.NewLogStore(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := ls.Append(ctx, "../escape", "n", []byte(`{}`)); err == nil {
		t.Fatal("Append with traversal runID succeeded")
	}
	if err := ls.Append(ctx, "r1", "../../escape", []byte(`{}`)); err == nil {
		t.Fatal("Append with traversal nodeID succeeded")
	}
	if _, err := ls.Read(ctx, "r1", "a/../../b", storage.ReadOpts{}); err == nil {
		t.Fatal("Read with traversal nodeID succeeded")
	}
	if err := ls.Append(ctx, "r1", "parent/shard-a", []byte(`{}`)); err != nil {
		t.Fatalf("Append with hierarchical (spawned) nodeID failed: %v", err)
	}
	if _, err := ls.ReadRun(ctx, ".."); err == nil {
		t.Fatal("ReadRun with traversal runID succeeded")
	}
	if err := ls.DeleteRun(ctx, "../logs"); err == nil {
		t.Fatal("DeleteRun with traversal runID succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "logs" {
		t.Fatalf("rejected IDs still created entries outside the log root: %v", entries)
	}
}

func TestArtifactStore_RejectsPathEscapingKeys(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	as, err := fs.NewArtifactStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	rejected := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"dot", "."},
		{"parent", ".."},
		{"leading parent", "../secret.txt"},
		{"nested parent", "../../etc/passwd"},
		{"interior parent", "runs/../../secret.txt"},
		{"absolute", "/etc/passwd"},
		{"empty segment", "runs//state.ndjson"},
		{"backslash", `..\secret.txt`},
		{"control character", "runs/\x00/state.ndjson"},
		{"shard escapes", "..secret"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := as.Get(ctx, tc.key); err == nil {
				t.Error("Get succeeded")
			}
			if err := as.Put(ctx, tc.key, bytes.NewReader([]byte("owned"))); err == nil {
				t.Error("Put succeeded")
			}
			if _, err := as.Has(ctx, tc.key); err == nil {
				t.Error("Has succeeded")
			}
			if err := as.Delete(ctx, tc.key); err == nil {
				t.Error("Delete succeeded")
			}
			if _, err := as.PutIfAbsent(ctx, tc.key, bytes.NewReader([]byte("owned"))); err == nil {
				t.Error("PutIfAbsent succeeded")
			}
		})
	}

	body, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("file outside the store root was rewritten: %q", body)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("rejected keys created entries outside the store root: %v", entries)
	}
}

func TestArtifactStore_AcceptsRealKeys(t *testing.T) {
	as, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, key := range []string{"a", "ab", "abcd1234", "runs/r1/state.ndjson", "bin/some-key"} {
		if err := as.Put(ctx, key, bytes.NewReader([]byte(key))); err != nil {
			t.Fatalf("Put %q: %v", key, err)
		}
		rc, err := as.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get %q: %v", key, err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != key {
			t.Fatalf("Get %q = %q", key, got)
		}
	}
}
