package logs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendLockShardsPerStoredFile(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	lock := func(runID, nodeID string) *sync.Mutex {
		return s.appendLock(nodePath(runID, nodeID))
	}
	if lock("run-1", "step-a") == lock("run-1", "step-b") {
		t.Error("step-a and step-b share an append mutex, so the slow-body test proves nothing about sharding")
	}
	if lock("run-1", "a/b") != lock("run-1", "a__b") {
		t.Error("two node ids that map to one file take different mutexes, so their byte caps race")
	}
}

func TestHasFreeSpaceFailsClosedWhenTheProbeFails(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.WithLimits(Limits{MinFreeBytes: 1})
	s.diskSpace = func(string) (uint64, uint64, bool) { return 0, 0, false }
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/logs/run-1/step-a", "text/plain", strings.NewReader("line\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("status=%d, want 507 when the free-space probe cannot measure the volume", resp.StatusCode)
	}
}

func TestSearchBudgetIsSpentAtItsDeadline(t *testing.T) {
	b := &searchBudget{ctx: t.Context(), deadline: time.Now().Add(-time.Second)}
	if !b.spent() {
		t.Error("a budget past its deadline reports itself unspent between files")
	}
}
