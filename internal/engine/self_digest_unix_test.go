//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetSelfDigestMemory() {
	selfDigest.Lock()
	selfDigest.info, selfDigest.digest = nil, ""
	selfDigest.Unlock()
}

func TestExecutableDigestRecordSurvivesProcessesButNotRewrites(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	previous := stableAfter
	stableAfter = 0
	t.Cleanup(func() { stableAfter = previous; resetSelfDigestMemory() })
	path := filepath.Join(t.TempDir(), "sparkwing")
	if err := os.WriteFile(path, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSelfDigestMemory()
	first, err := executableDigest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	record := selfDigestRecord(path)
	body, err := os.ReadFile(record)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(body)), " "+first) {
		t.Fatalf("record = %q, %v; want the digest under the file identity", body, err)
	}

	// safety: a planted digest under the current identity stands in for an earlier process's record.
	planted := strings.Repeat("a", 64)
	identity, _, _ := strings.Cut(strings.TrimSpace(string(body)), " ")
	if err := os.WriteFile(record, []byte(identity+" "+planted+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetSelfDigestMemory()
	if got, err := executableDigest(context.Background(), path); err != nil || got != planted {
		t.Fatalf("digest = %s, %v; want the recorded digest for an unchanged file", got, err)
	}

	if err := os.WriteFile(path, []byte("two"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSelfDigestMemory()
	got, err := executableDigest(context.Background(), path)
	if err != nil || got == planted || got == first {
		t.Fatalf("digest after rewrite = %s, %v; want a fresh hash", got, err)
	}
}

func TestExecutableDigestRecordsDoNotEvictAnotherExecutable(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	previous := stableAfter
	stableAfter = 0
	t.Cleanup(func() { stableAfter = previous; resetSelfDigestMemory() })
	installed := filepath.Join(t.TempDir(), "sparkwing")
	retained := filepath.Join(t.TempDir(), "sparkwing")
	for _, path := range []string{installed, retained} {
		if err := os.WriteFile(path, []byte("same executable"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	resetSelfDigestMemory()
	if _, err := executableDigest(t.Context(), installed); err != nil {
		t.Fatal(err)
	}
	record := selfDigestRecord(installed)
	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, _ := strings.Cut(strings.TrimSpace(string(body)), " ")
	planted := strings.Repeat("a", 64)
	if err := os.WriteFile(record, []byte(identity+" "+planted+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetSelfDigestMemory()
	if _, err := executableDigest(t.Context(), retained); err != nil {
		t.Fatal(err)
	}
	resetSelfDigestMemory()
	if got, err := executableDigest(t.Context(), installed); err != nil || got != planted {
		t.Fatalf("another executable evicted the installed digest: %s, %v", got, err)
	}
}
