package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func processReadChars(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "rchar: "); ok {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("missing rchar in /proc/self/io")
	return 0
}

func TestShortLocalTailReadsOnlyTheSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	data := strings.Repeat("ordinary log line\n", 1<<20)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	before := processReadChars(t)
	if err := writeFile(path, LogsOpts{Tail: 40, Format: "plain"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	read := processReadChars(t) - before
	t.Logf("tail read %d bytes from %d-byte log", read, len(data))
	if read > 128*1024 {
		t.Fatalf("short tail read %d bytes from a %d-byte log; want bounded suffix reads", read, len(data))
	}
}
