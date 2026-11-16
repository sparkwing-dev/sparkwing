package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type sealSummaryRenewal struct {
	diagnosticNodeRenewal
	calls atomic.Int64
}

func (r *sealSummaryRenewal) RenewNodeClaim(context.Context, runspec.NodeRef, runstate.Actor) (runstate.LeaseBeat, error) {
	r.calls.Add(1)
	return runstate.LeaseBeat{}, nil
}

type attemptLogs struct {
	mu      sync.Mutex
	records map[int64]*nativeLogRecords
	opens   map[int64]int
}

func (a *attemptLogs) open(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := subject.Attempt.Attempt
	a.opens[attempt]++
	if a.records[attempt] == nil {
		a.records[attempt] = &nativeLogRecords{objects: map[string][]byte{}, view: runstate.LogSegments{Subject: subject}}
	}
	return a.records[attempt], nil
}

func (a *attemptLogs) writers() map[int64]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return maps.Clone(a.opens)
}

type keeperLine struct {
	Host  bool `json:"host_lease_keeper"`
	Attrs struct {
		Kind     string `json:"kind"`
		Sequence uint64 `json:"sequence"`
		Summary  *struct {
			OpenMonoBefore int64  `json:"open_mono_before_ns"`
			SealMonoAfter  int64  `json:"seal_mono_after_ns"`
			AsOfSeal       bool   `json:"as_of_seal"`
			DrainComplete  bool   `json:"drain_complete"`
			LastDelivered  uint64 `json:"last_delivered_sequence"`
			Captured       uint64 `json:"captured"`
			Lost           uint64 `json:"lost"`
		} `json:"summary"`
	} `json:"attrs"`
}

func (a *attemptLogs) sealedKeeperLines(ctx context.Context, t *testing.T, attempt int64) (data []keeperLine, summary keeperLine) {
	t.Helper()
	a.mu.Lock()
	records := a.records[attempt]
	a.mu.Unlock()
	if records == nil || !records.view.Complete {
		t.Fatalf("attempt %d log is not sealed complete", attempt)
	}
	var body bytes.Buffer
	if _, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(ctx, runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range strings.Split(strings.TrimSpace(body.String()), "\n") {
		if raw == "" {
			continue
		}
		var line keeperLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		if !line.Host {
			continue
		}
		if found {
			t.Fatalf("attempt %d has keeper records after its summary: %s", attempt, body.String())
		}
		if line.Attrs.Kind == "summary" {
			found, summary = true, line
			continue
		}
		data = append(data, line)
	}
	if !found || summary.Attrs.Summary == nil || !summary.Attrs.Summary.AsOfSeal {
		t.Fatalf("attempt %d sealed without a keeper summary: %s", attempt, body.String())
	}
	return data, summary
}

func TestNativeKeeperSealSummaryPreservesLeaseThroughFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, _ := nativeConfigFixture(t)
		logs := &attemptLogs{records: map[int64]*nativeLogRecords{}, opens: map[int64]int{}}
		cfg.Logs = logs.open
		var leaseContext context.Context
		cfg.Store = completingUnitStore{NativeHostStore: cfg.Store, commit: func(ctx context.Context) error {
			if ctx.Err() != nil || leaseContext.Err() != nil {
				t.Fatal("lease ended before the finish write")
			}
			return nil
		}}
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
			return &processListener{closed: make(chan struct{})}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var closeOnce sync.Once
		closeServices := func() {
			closeOnce.Do(func() {
				if err := services.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		defer closeServices()
		services.Config.Lease.Lease, services.Config.Lease.Interval = time.Minute, time.Second
		renewal := &sealSummaryRenewal{}
		var stop func()
		leaseContext, stop = keepNode(t.Context(), services.Config.Lease, renewal,
			runspec.NodeRef{RunID: "run", NodeID: "job"}, runstate.Actor{NodeFence: runspec.NodeFence{Generation: 2}})
		defer stop()
		if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 3}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, err := services.Config.Store.TransitionNode(t.Context(), store.ReportNode{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		data, summary := logs.sealedKeeperLines(t.Context(), t, 3)
		last := data[len(data)-1].Attrs.Sequence
		// safety: concurrent keeper goroutines can drop a capture on contention; the summary must account for it.
		if s := summary.Attrs.Summary; s.DrainComplete != (s.Lost == 0) || s.Captured != last || s.LastDelivered != last {
			t.Fatalf("summary does not close the delivered window: %+v after sequence %d", s, last)
		}
		calls := renewal.calls.Load()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if leaseContext.Err() != nil || renewal.calls.Load() <= calls {
			t.Fatal("diagnostic seal stopped renewal")
		}
		closeServices()
		synctest.Wait()
		if writers := logs.writers(); writers[3] != 1 || len(writers) != 1 {
			t.Fatalf("diagnostics opened a replacement writer after detachment: %v", writers)
		}
	})
}

func TestNativeKeeperRetrySealsEachAttemptWindowBeforeTheFenceAdvances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, _ := nativeConfigFixture(t)
		logs := &attemptLogs{records: map[int64]*nativeLogRecords{}, opens: map[int64]int{}}
		cfg.Logs = logs.open
		var commits atomic.Int64
		cfg.Store = completingUnitStore{NativeHostStore: cfg.Store, commit: func(ctx context.Context) error {
			if commits.Add(1) == 1 {
				// safety: the retry commit is where the writer fence advances.
				logs.sealedKeeperLines(ctx, t, 3)
			}
			return nil
		}}
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
			return &processListener{closed: make(chan struct{})}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := services.Close(); err != nil {
				t.Error(err)
			}
		}()
		services.Config.Lease.Lease, services.Config.Lease.Interval = time.Minute, time.Second
		_, stop := keepNode(t.Context(), services.Config.Lease, &sealSummaryRenewal{},
			runspec.NodeRef{RunID: "run", NodeID: "job"}, runstate.Actor{NodeFence: runspec.NodeFence{Generation: 2}})
		defer stop()
		ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
		if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 3}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, err := services.Config.Store.TransitionNode(t.Context(), store.RetryNode{Ref: ref}); err != nil {
			t.Fatal(err)
		}
		if commits.Load() != 1 {
			t.Fatal("retry did not commit")
		}
		time.Sleep(3 * time.Second)
		if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 4}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if _, err := services.Config.Store.TransitionNode(t.Context(), store.ReportNode{Ref: ref}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		_, first := logs.sealedKeeperLines(t.Context(), t, 3)
		data, second := logs.sealedKeeperLines(t.Context(), t, 4)
		if len(data) == 0 || data[0].Attrs.Sequence != first.Attrs.Summary.Captured+1 {
			t.Fatalf("next attempt did not resume the keeper sequence after %d: %+v", first.Attrs.Summary.Captured, data)
		}
		s := second.Attrs.Summary
		if s.DrainComplete != (s.Lost == first.Attrs.Summary.Lost) || s.Captured != data[len(data)-1].Attrs.Sequence || s.LastDelivered != s.Captured || s.OpenMonoBefore <= first.Attrs.Summary.SealMonoAfter {
			t.Fatalf("second window summary %+v after first %+v", s, first.Attrs.Summary)
		}
		if writers := logs.writers(); writers[3] != 1 || writers[4] != 1 || len(writers) != 2 {
			t.Fatalf("writers per attempt: %v", writers)
		}
	})
}
