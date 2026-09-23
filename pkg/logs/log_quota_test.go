package logs

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota/storagequotatest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
// drawing appends from a block it reserved with the appending caller's own
// credential; an append past the share writes nothing, and a settle commits
// what was written and gives the rest of the block back.
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
	f.srv.settleLogBlocks(context.Background(), true)
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
	f.srv.settleLogBlocks(context.Background(), true)
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

func (f *archiveFixture) appendAs(t *testing.T, bearer, runID, nodeID string, gen int, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.http.URL+"/api/v1/logs/"+runID+"/"+nodeID, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	if gen > 0 {
		req.Header.Set(store.ClaimHolderHeader, "agent:a")
		req.Header.Set(store.ClaimMembershipHeader, "membership-a")
		req.Header.Set(store.ClaimReservationHeader, "reservation-a")
		req.Header.Set(store.ClaimGenerationHeader, strconv.Itoa(gen))
		req.Header.Set(store.AttemptOrdinalHeader, "1")
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// Ten thousand one-line appends of one attempt cost the controller one claim
// check and one storage call per mebibyte, not three calls a line, and a
// settle commits every byte they stored.
func TestTenThousandLinesCostAHandfulOfControllerCalls(t *testing.T) {
	counter := storagequotatest.New(1<<30, 0)
	f := newArchiveFixtureWith(t, 0, counter)
	const lines, width = 10000, 200
	for i := range lines {
		if code := f.appendAs(t, "Bearer a", "run-a", "build", 1, line(width)); code != http.StatusNoContent {
			t.Fatalf("append %d = %d", i, code)
		}
	}
	total := int64(lines * width)
	storage := f.calls.matching("/internal/storage/")
	claims := f.calls.matching("/claim/validate")
	if want := int(total/(1<<20)) + 1; storage > want {
		t.Errorf("%d lines made %d storage calls, want at most %d: one per MiB", lines, storage, want)
	}
	if claims > 1 {
		t.Errorf("%d lines of one attempt made %d claim checks, want 1 inside the cache window", lines, claims)
	}
	if used, reserved := counter.Held("team-a", storagequota.KindLogs); used+reserved < total {
		t.Fatalf("the controller holds %d used and %d reserved for %d stored bytes", used, reserved, total)
	}

	f.srv.settleLogBlocks(context.Background(), false)
	if used, reserved := counter.Held("team-a", storagequota.KindLogs); used != total || reserved != LogBlockBytes {
		t.Fatalf("after a settle of an active run = %d used, %d reserved; want %d and a fresh block", used, reserved, total)
	}
	f.srv.settleLogBlocks(context.Background(), false)
	if used, reserved := counter.Held("team-a", storagequota.KindLogs); used != total || reserved != 0 {
		t.Fatalf("after a settle of an idle run = %d used, %d reserved; want %d and the block given back", used, reserved, total)
	}
}

// A confirmed claim is reused only for the exact claim it confirmed: another
// generation, and an append that names none, is checked again every time.
func TestAConfirmedClaimCoversOnlyItsOwnAttempt(t *testing.T) {
	f := newArchiveFixtureWith(t, 0, storagequotatest.New(1<<30, 0))
	for range 3 {
		if code := f.appendAs(t, "Bearer a", "run-a", "build", 1, line(10)); code != http.StatusNoContent {
			t.Fatal(code)
		}
	}
	if got := f.calls.matching("/claim/validate"); got != 1 {
		t.Fatalf("three appends of one attempt = %d claim checks, want 1", got)
	}
	if code := f.appendAs(t, "Bearer a", "run-a", "build", 2, line(10)); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if got := f.calls.matching("/claim/validate"); got != 2 {
		t.Fatalf("a new generation = %d claim checks in all, want it checked again", got)
	}
	for range 2 {
		if code := f.appendAs(t, "Bearer a", "run-a", "build", 0, line(10)); code != http.StatusNoContent {
			t.Fatal(code)
		}
	}
	if got := f.calls.matching("/claim/validate"); got != 4 {
		t.Fatalf("two appends naming no generation = %d claim checks in all, want each checked", got)
	}
}
