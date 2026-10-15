package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func FuzzOutputReadRequiresExactBytes(f *testing.F) {
	f.Add([]byte(`{"v":1}`), []byte(`{"v":2}`))
	f.Add([]byte("value"), []byte(""))
	f.Fuzz(func(t *testing.T, committed, actual []byte) {
		if int64(len(committed)) > limits.NodeOutputBytes || int64(len(actual)) > limits.NodeOutputBytes {
			t.Skip()
		}
		dir := t.TempDir()
		key := "outputs/run/job/a1-g1-output"
		path := OutputPath(dir, key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(committed)
		ref := OutputRef{Key: key, Size: int64(len(committed)), SHA256: hex.EncodeToString(sum[:])}
		got, err := ReadOutputFile(dir, ref)
		if bytes.Equal(committed, actual) {
			if err != nil || !bytes.Equal(got, committed) {
				t.Fatalf("matching output = %q, %v", got, err)
			}
		} else if !errors.Is(err, ErrOutputCorrupt) || got != nil {
			t.Fatalf("mismatched output = %q, %v", got, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadOutputFile(dir, ref); !errors.Is(err, ErrNotFound) || got != nil {
			t.Fatalf("missing output = %q, %v", got, err)
		}
	})
}

func FuzzOutputReferenceUsesSharedLimit(f *testing.F) {
	for _, size := range []int64{0, 1, limits.NodeOutputBytes - 1, limits.NodeOutputBytes, limits.NodeOutputBytes + 1, 64 << 20} {
		f.Add(size)
	}
	f.Fuzz(func(t *testing.T, size int64) {
		ref := OutputRef{Key: "outputs/run/job/a1-g1-output", Size: size, SHA256: strings.Repeat("a", 64)}
		if got, want := ref.valid("run", "job"), size > 0 && size <= limits.NodeOutputBytes; got != want {
			t.Fatalf("size=%d valid=%v, want %v", size, got, want)
		}
	})
}

func TestOutputFileRejectsOversizedReferenceBeforeOpening(t *testing.T) {
	ref := OutputRef{Key: "outputs/run/job/a1-g1-output", Size: limits.NodeOutputBytes + 1, SHA256: strings.Repeat("a", 64)}
	value, err := ReadOutputFile(filepath.Join(t.TempDir(), "absent"), ref)
	if len(value) != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("value=%d bytes, err=%v", len(value), err)
	}
}
