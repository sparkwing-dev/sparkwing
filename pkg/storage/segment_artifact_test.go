package storage

import "testing"

func TestSafeArtifactKey(t *testing.T) {
	for _, key := range []string{"a", "ab", "abcd1234", "runs/r1/state.ndjson", "bin/some-key", "artifacts/blobs/0f1e"} {
		if err := SafeArtifactKey(key); err != nil {
			t.Errorf("SafeArtifactKey(%q) = %v, want nil", key, err)
		}
	}
	for _, key := range []string{"", ".", "..", "..x", "...", ".hidden", "a/.b", "%2e%2e%2fetc%2fpasswd", "a#frag", "a?delete=1", "a b", "caf\u00e9", "a//b", "/abs", "a\\b"} {
		if err := SafeArtifactKey(key); err == nil {
			t.Errorf("SafeArtifactKey(%q) = nil, want error", key)
		}
	}
}
