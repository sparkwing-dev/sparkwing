package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUsageReporter_UsageTotalsStoredBytesAndObjects(t *testing.T) {
	t.Parallel()
	s, err := NewUsageReporter(t.TempDir())
	if err != nil {
		t.Fatalf("NewUsageReporter: %v", err)
	}
	ctx := context.Background()
	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage on an empty store: %v", err)
	}
	if usage.Bytes != 0 || usage.Objects != 0 || usage.Partial || usage.ObservedAt.IsZero() {
		t.Fatalf("empty store usage = %+v", usage)
	}

	for key, body := range map[string]string{"teams/acme/cas/blob": "12345", "logs/r90/teams/acme/run/segment": "abc"} {
		path := filepath.Join(s.Root, key)
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
