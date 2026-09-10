//go:build unix

package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
)

func TestBinaryUploadReportsDiskWriteFailures(t *testing.T) {
	const childEnv = "SPARKWING_TEST_BIN_WRITE_LIMIT"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBinaryUploadReportsDiskWriteFailures$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("limited-file subprocess: %v\n%s", err, out)
		}
		return
	}
	binsDir = t.TempDir()
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: 1}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handleBin(response, httptest.NewRequest(http.MethodPut, "/bin/deadbeef", strings.NewReader("binary")))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("disk write failure status=%d, want500", response.Code)
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed disk write left staging: %v %v", entries, err)
	}
}
