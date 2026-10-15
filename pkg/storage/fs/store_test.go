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

	if err := s.PutIfAbsent(ctx, "abcd1234", bytes.NewReader([]byte("hello"))); err != nil {
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

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "abcd1234"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "abcd1234"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if err := s.Delete(ctx, "abcd1234"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestArtifactStore_AtomicPut(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, _ := NewArtifactStore(root)
	ctx := context.Background()

	for i := range 5 {
		key := "abc" + string(rune('A'+i))
		if err := s.PutIfAbsent(ctx, key, bytes.NewReader([]byte("payload"))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
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
		if err := s.PutIfAbsent(ctx, key, bytes.NewReader([]byte("x"))); err != nil {
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

func TestArtifactStore_UsageTotalsStoredBytesAndObjects(t *testing.T) {
	t.Parallel()
	s, err := NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	ctx := context.Background()
	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage on an empty store: %v", err)
	}
	if usage.Bytes != 0 || usage.Objects != 0 || usage.Partial || usage.ObservedAt.IsZero() {
		t.Fatalf("empty store usage = %+v", usage)
	}

	for key, body := range map[string]string{"runs/abc/state.ndjson": "12345", "bin/some-key": "abc"} {
		if err := s.PutIfAbsent(ctx, key, strings.NewReader(body)); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	usage, err = s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Bytes != 8 || usage.Objects != 2 || usage.Partial {
		t.Fatalf("usage = %+v, want 8 bytes in 2 objects", usage)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	usage, err = s.Usage(cancelled)
	if err != nil {
		t.Fatalf("Usage under a cancelled context: %v", err)
	}
	if !usage.Partial {
		t.Fatalf("usage under a cancelled context = %+v, want partial", usage)
	}
}
