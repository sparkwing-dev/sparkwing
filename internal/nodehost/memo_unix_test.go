//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestMemo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "attemptfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/attemptfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	srv, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	st := openStore(t)
	claim := outputController(t, st)
	marker := t.TempDir()
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	memo := &Memo{
		Slots: storeSlots{st: st},
		Output: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) ([]byte, error) {
			return st.GetNodeOutput(ctx, runID, nodeID)
		},
	}
	daemon := newFakeDaemon()
	groupSlots := &memoGroupSlots{storeSlots: storeSlots{st: st}}
	host := func(runID string) RunResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		res, err := Execute(ctx, Config{
			Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second},
			Pipeline: "attempts", Args: map[string]string{"memo": "true", "marker": marker},
			Run:   nodeproto.RunContext{RunID: runID},
			State: st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
			Attempts: &Attempts{
				Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(), Claim: claim,
				OpenLog: func(_ context.Context, run, node string) (NodeLog, error) {
					return memLog{logs: logs, node: run + "/" + node}, nil
				},
			},
			Memo:      memo,
			Admission: &Admission{Daemon: daemon, Slots: groupSlots},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first, second := host("run-memo-1"), host("run-memo-2")

	row := func(t *testing.T, runID, node string) *store.Node {
		t.Helper()
		r, err := st.GetNode(t.Context(), runID, node)
		if err != nil {
			t.Fatalf("node %s/%s: %v", runID, node, err)
		}
		return r
	}
	output := func(t *testing.T, runID, node string) string {
		t.Helper()
		out, err := st.GetNodeOutput(t.Context(), runID, node)
		if err != nil {
			t.Fatalf("output of %s/%s: %v", runID, node, err)
		}
		return string(out)
	}
	bodies := func(node string) int {
		data, _ := os.ReadFile(filepath.Join(marker, node))
		return strings.Count(string(data), "body")
	}

	t.Run("AMissRunsAndRecordsAndAHitCopiesWithoutRunning", func(t *testing.T) {
		if r := row(t, "run-memo-1", "memoized"); r.Outcome != string(sparkwing.Success) {
			t.Errorf("first run: memoized outcome=%q error=%q, want success", r.Outcome, r.Error)
		}
		r := row(t, "run-memo-2", "memoized")
		if r.Outcome != string(sparkwing.Cached) || r.FailureReason != "" || r.StartedAt == nil {
			t.Errorf("second run: memoized outcome=%q reason=%q started=%v, want cached and started", r.Outcome, r.FailureReason, r.StartedAt)
		}
		if got, want := output(t, "run-memo-2", "memoized"), output(t, "run-memo-1", "memoized"); got != want || !strings.Contains(got, "run-memo-1") {
			t.Errorf("second run output %s, want the first run's %s", got, want)
		}
		if n := bodies("memoized"); n != 1 {
			t.Errorf("memoized body ran %d times, want once", n)
		}
	})

	t.Run("AHitWritesTheDispatchersCacheHitEvent", func(t *testing.T) {
		events, err := st.ListEventsAfter(t.Context(), "run-memo-2", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]string
		for _, e := range events {
			if e.NodeID == "memoized" && e.Kind == "cache_hit" {
				if err := json.Unmarshal(e.Payload, &payload); err != nil {
					t.Fatal(err)
				}
			}
		}
		want := map[string]string{"key": "memo:memo-v1", "cache_key_hash": "memo-v1", "origin_run_id": "run-memo-1", "origin_node_id": "memoized"}
		if !maps.Equal(payload, want) {
			t.Errorf("cache_hit payload = %v, want %v", payload, want)
		}
		var end map[string]any
		logs.mu.Lock()
		for _, r := range logs.records["run-memo-2/memoized"] {
			if r.Event == "node_end" {
				end = r.Attrs
			}
		}
		logs.mu.Unlock()
		if end["cache_hit"] != true || end["outcome"] != string(sparkwing.Cached) {
			t.Errorf("memoized node_end attrs = %v, want a cached cache_hit", end)
		}
	})

	t.Run("AHitTakesNoAdmissionLeaseOrGroupSlot", func(t *testing.T) {
		leases := map[string]int{}
		daemon.mu.Lock()
		for _, req := range daemon.reqs {
			leases[req.DisplayRunID]++
		}
		daemon.mu.Unlock()
		if leases["run-memo-1/memoized"] != 1 || leases["run-memo-2/memoized"] != 0 {
			t.Errorf("memoized leases %v, want one in the first run and none for the hit", leases)
		}
		groupSlots.mu.Lock()
		defer groupSlots.mu.Unlock()
		if got := groupSlots.acquired; !slices.Equal(got, []string{"run-memo-1"}) {
			t.Errorf("memo-group acquired by runs %v, want the first run only", got)
		}
	})

	t.Run("AFailureRecordsNothing", func(t *testing.T) {
		if first.Nodes["memo-flaky"].Outcome != sparkwing.Failed || second.Nodes["memo-flaky"].Outcome != sparkwing.Success {
			t.Errorf("memo-flaky = %+v then %+v, want a failure, then a fresh success", first.Nodes["memo-flaky"], second.Nodes["memo-flaky"])
		}
		if n := bodies("memo-flaky"); n != 2 {
			t.Errorf("memo-flaky body ran %d times, want twice", n)
		}
	})

	t.Run("NoCacheRunsEveryTime", func(t *testing.T) {
		if second.Nodes["nocache"].Outcome != sparkwing.Success || bodies("nocache") != 2 {
			t.Errorf("nocache = %+v after %d bodies, want a success from a second body", second.Nodes["nocache"], bodies("nocache"))
		}
		if log := logs.text("run-memo-2/nocache"); !strings.Contains(log, "Memoize(nocache) returned NoCache; memoization skipped") {
			t.Errorf("nocache log lacks the NoCache note:\n%s", log)
		}
	})

	t.Run("AnEmptyKeyOrAKeyErrorFailsBeforeTheNodeStarts", func(t *testing.T) {
		for node, msg := range map[string]string{
			"emptykey": "resolve cache key for emptykey: empty key",
			"keyfail":  "resolve cache key for keyfail: no lockfile",
		} {
			r := row(t, "run-memo-1", node)
			if r.Outcome != string(sparkwing.Failed) || r.Error != msg || r.StartedAt != nil {
				t.Errorf("%s outcome=%q error=%q started=%v, want failed with %q and never started", node, r.Outcome, r.Error, r.StartedAt, msg)
			}
			if n := bodies(node); n != 0 {
				t.Errorf("%s body ran %d times", node, n)
			}
		}
	})

	t.Run("AMemoizedSourceStillFansOut", func(t *testing.T) {
		if r := row(t, "run-memo-2", "memo-targets"); r.Outcome != string(sparkwing.Cached) {
			t.Errorf("source outcome=%q error=%q, want cached", r.Outcome, r.Error)
		}
		if n := bodies("memo-targets"); n != 1 {
			t.Errorf("source body ran %d times, want once", n)
		}
		for _, id := range []string{"memo-test-x", "memo-test-y", "memo-summary"} {
			if r := row(t, "run-memo-2", id); r.Outcome != string(sparkwing.Success) {
				t.Errorf("second run %s outcome=%q error=%q, want success", id, r.Outcome, r.Error)
			}
		}
		if r := row(t, "run-memo-2", "memo-summary"); strings.Join(r.Deps, ",") != "memo-test-x,memo-test-y" {
			t.Errorf("summary deps = %v, want the generated nodes", r.Deps)
		}
	})
}

type memoGroupSlots struct {
	storeSlots
	mu       sync.Mutex
	acquired []string
}

func (s *memoGroupSlots) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if strings.Contains(req.Key, "memo-group") {
		s.mu.Lock()
		s.acquired = append(s.acquired, req.RunID)
		s.mu.Unlock()
	}
	return s.storeSlots.AcquireSlot(ctx, req)
}
