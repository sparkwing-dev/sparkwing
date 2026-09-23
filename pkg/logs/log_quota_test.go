package logs

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota/storagequotatest"
)

func (f *archiveFixture) logSize(t *testing.T, runID, nodeID string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(f.root, "runs", runID, nodeID+".log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func line(n int) string { return strings.Repeat("x", n-1) + "\n" }

// The logs service holds a free team to the share the controller counts,
// with the appending caller's own credential, and an append the controller
// refuses writes nothing.
func TestAFreeTeamsLogsStopAtTheirShare(t *testing.T) {
	counter := storagequotatest.New(300, 0)
	f := newArchiveFixtureWith(t, 0, counter)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(200)); code != http.StatusNoContent {
		t.Fatalf("200 of 300 = %d %s", code, body)
	}
	code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(101))
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "add credits") {
		t.Fatalf("101 more past the share = %d %q, want 413 naming the remedy", code, body)
	}
	if got := f.logSize(t, "run-a", "build"); got != 200 {
		t.Fatalf("the log holds %d bytes after a refused append, want 200", got)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(100)); code != http.StatusNoContent {
		t.Fatalf("the last 100 bytes = %d %s", code, body)
	}
	if used, reserved := counter.Held("team-a", storagequota.KindLogs); used != 300 || reserved != 0 {
		t.Fatalf("the controller counts %d used, %d reserved; want 300 and nothing held", used, reserved)
	}
	for _, auth := range counter.Auths() {
		if auth != "Bearer a" {
			t.Fatalf("a counter call carried %q, want the appending caller's credential", auth)
		}
	}
}

// A write that fails after its reservation gives the reservation back, so
// the team keeps the rest of its share: nothing is charged for bytes that
// were never written.
func TestAFailedAppendHoldsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	counter := storagequotatest.New(300, 0)
	f := newArchiveFixtureWith(t, 0, counter)
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-b/first", "Bearer b", line(10)); code != http.StatusNoContent {
		t.Fatalf("first append = %d", code)
	}
	dir := filepath.Join(f.root, "runs", "run-b")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-b/second", "Bearer b", line(250))
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("an append the volume refused = %d, want 500", code)
	}
	if used, reserved := counter.Held("team-b", storagequota.KindLogs); used != 10 || reserved != 0 {
		t.Fatalf("after a failed append the controller counts %d used, %d reserved; want 10 and nothing held", used, reserved)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-b/third", "Bearer b", line(290)); code != http.StatusNoContent {
		t.Fatalf("the rest of the share after a failed append = %d %s", code, body)
	}
}

// A controller that cannot count refuses a free team's append with 503 and
// writes nothing, and the operator, whom no share holds, appends on.
func TestAppendsFailClosedWhileTheControllerCannotCount(t *testing.T) {
	counter := storagequotatest.New(300, 0)
	counter.SetDown(true)
	f := newArchiveFixtureWith(t, 0, counter)
	code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(10))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a free team's append with the counter down = %d %q, want 503", code, body)
	}
	if got := f.logSize(t, "run-a", "build"); got != 0 {
		t.Fatalf("a refused append wrote %d bytes", got)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-op/build", "Bearer admin", line(10)); code != http.StatusNoContent {
		t.Fatalf("the operator's append with the counter down = %d %s", code, body)
	}
	counter.SetDown(false)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(10)); code != http.StatusNoContent {
		t.Fatalf("a free team's append with the counter back = %d %s", code, body)
	}
}
