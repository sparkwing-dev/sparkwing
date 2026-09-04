package logs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAppendLockShardsPerLogicalNode(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	lock := func(runID, nodeID string) *sync.Mutex {
		return s.appendNodeLock(runID, nodeID)
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

func TestAttemptSubstreamsMergeByOrdinalAcrossExecutorKinds(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	appendLog := func(body string, headers map[string]string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logs/run-1/build", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("append %q status = %d, want 204", body, resp.StatusCode)
		}
	}
	appendLog("trigger-1\n", map[string]string{
		store.TriggerGenerationHeader: "4", store.AttemptOrdinalHeader: "1",
	})
	appendLog("assisted-2\n", map[string]string{
		store.ClaimHolderHeader: "agent:a:1", store.ClaimGenerationHeader: "8", store.AttemptOrdinalHeader: "2",
	})
	appendLog("trigger-3\n", map[string]string{
		store.TriggerGenerationHeader: "4", store.AttemptOrdinalHeader: "3",
	})

	resp, err := http.Get(srv.URL + "/api/v1/logs/run-1/build")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "trigger-1\nassisted-2\ntrigger-3\n"; got != want {
		t.Fatalf("merged log = %q, want %q", got, want)
	}
}
