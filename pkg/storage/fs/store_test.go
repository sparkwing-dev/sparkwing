package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

func TestArtifactStore_RoundTrip(t *testing.T) {
	t.Parallel()
	s, err := NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	ctx := context.Background()

	if err := s.Put(ctx, "abcd1234", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "abcd1234")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Fatalf("Get = %q, want hello", got)
	}

	has, err := s.Has(ctx, "abcd1234")
	if err != nil || !has {
		t.Fatalf("Has = (%v, %v), want (true, nil)", has, err)
	}
	has, _ = s.Has(ctx, "missing")
	if has {
		t.Fatalf("Has(missing) = true")
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "abcd1234"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if has, _ := s.Has(ctx, "abcd1234"); has {
		t.Fatalf("Has after Delete = true")
	}
	if err := s.Delete(ctx, "abcd1234"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestArtifactStore_AtomicPut(t *testing.T) {
	// Verify rename-from-tmp pattern: no .put-* tempfiles linger.
	t.Parallel()
	root := t.TempDir()
	s, _ := NewArtifactStore(root)
	ctx := context.Background()

	for i := range 5 {
		key := "abc" + string(rune('A'+i))
		if err := s.Put(ctx, key, bytes.NewReader([]byte("payload"))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	// shard dir is "ab"
	entries, err := os.ReadDir(root + "/ab")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".put-") {
			t.Errorf("leftover tempfile: %s", e.Name())
		}
	}
}

func TestArtifactStore_List(t *testing.T) {
	t.Parallel()
	s, err := NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	ctx := context.Background()

	put := func(key string) {
		t.Helper()
		if err := s.Put(ctx, key, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	put("runs/abc/state.ndjson")
	put("runs/def/state.ndjson")
	put("runs/ghi/extra.bin")
	put("bin/some-key")

	got, err := s.List(ctx, "runs/")
	if err != nil {
		t.Fatalf("List runs/: %v", err)
	}
	want := map[string]bool{
		"runs/abc/state.ndjson": true,
		"runs/def/state.ndjson": true,
		"runs/ghi/extra.bin":    true,
	}
	if len(got) != len(want) {
		t.Fatalf("List runs/ = %v, want %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}

	got, _ = s.List(ctx, "")
	if len(got) != 4 {
		t.Fatalf("List \"\" = %v keys, want 4", len(got))
	}

	got, _ = s.List(ctx, "missing/")
	if len(got) != 0 {
		t.Fatalf("List missing/ = %v, want empty", got)
	}
}

func TestLogStore_RoundTrip(t *testing.T) {
	t.Parallel()
	ls, err := NewLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	ctx := context.Background()

	if err := ls.Append(ctx, "run1", "n1", []byte(`{"msg":"hello"}`+"\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ls.Append(ctx, "run1", "n1", []byte(`{"msg":"world"}`+"\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ls.Append(ctx, "run1", "n2", []byte(`{"msg":"alpha"}`+"\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ls.Read(ctx, "run1", "n1", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(string(got), "hello") || !strings.Contains(string(got), "world") {
		t.Fatalf("Read = %q, want both records", got)
	}

	got, err = ls.Read(ctx, "run1", "n1", storage.ReadOpts{Tail: 1})
	if err != nil {
		t.Fatalf("Read tail: %v", err)
	}
	if strings.Contains(string(got), "hello") || !strings.Contains(string(got), "world") {
		t.Fatalf("Read tail=1 = %q, want only last record", got)
	}

	got, err = ls.Read(ctx, "run1", "n1", storage.ReadOpts{Grep: "world"})
	if err != nil {
		t.Fatalf("Read grep: %v", err)
	}
	if strings.Contains(string(got), "hello") || !strings.Contains(string(got), "world") {
		t.Fatalf("Read grep=world = %q", got)
	}

	got, err = ls.Read(ctx, "run1", "missing-node", storage.ReadOpts{})
	if err != nil || got != nil {
		t.Fatalf("Read missing = (%q, %v)", got, err)
	}

	got, err = ls.ReadRun(ctx, "run1")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if !strings.Contains(string(got), "=== n1 ===") || !strings.Contains(string(got), "=== n2 ===") {
		t.Fatalf("ReadRun = %q, want banners for both nodes", got)
	}

	if err := ls.DeleteRun(ctx, "run1"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	got, _ = ls.ReadRun(ctx, "run1")
	if len(got) != 0 {
		t.Fatalf("ReadRun after delete = %q", got)
	}
	if err := ls.DeleteRun(ctx, "run1"); err != nil {
		t.Fatalf("DeleteRun idempotent: %v", err)
	}
}

func TestLogStore_Stream_NoOp(t *testing.T) {
	t.Parallel()
	ls, _ := NewLogStore(t.TempDir())
	rc, err := ls.Stream(context.Background(), "r", "n")
	if err != nil || rc != nil {
		t.Fatalf("Stream = (%v, %v), want (nil, nil)", rc, err)
	}
}
