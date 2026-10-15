package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
		path, err := relPath(key)
		if err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(s.Root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
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
