package lease

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestKeeperSealSummaryAccountsForEachLossSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error {
			close(entered)
			<-release
			return errors.New("delivery failed")
		})
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "OWNER", Run: "loss-run"})
		trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
		<-entered
		for range 258 {
			trace.emit("watch_reset", time.Time{}, time.Now().Add(time.Minute), "", 1)
		}
		trace.mu.Lock()
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		trace.mu.Unlock()
		var summaries []Event
		delivered := 0
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		result := make(chan bool)
		go func() {
			result <- diagnostics.Seal(ctx, func(_ context.Context, event Event) error {
				if event.Summary != nil {
					summaries = append(summaries, event)
				} else {
					delivered++
				}
				return nil
			})
		}()
		synctest.Wait()
		close(release)
		complete := <-result
		if complete || len(summaries) != 1 || delivered != 256 {
			t.Fatalf("complete=%v summaries=%d delivered=%d", complete, len(summaries), delivered)
		}
		// safety: one delivery failure, two queue overflows and one contended capture.
		summary := summaries[0].Summary
		if summary.Captured != 259 || summary.LastDeliveredSequence != 257 || summary.Lost != 4 || summary.DrainComplete {
			t.Fatalf("loss accounting: %+v", summary)
		}
	})
}

func TestKeeperSealSummaryFollowsEveryDrainedRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "order"})
		trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
		synctest.Wait()
		diagnostics.Cutoff()
		var kinds []string
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if !diagnostics.Seal(ctx, func(_ context.Context, event Event) error {
			kinds = append(kinds, event.Kind)
			return nil
		}) {
			t.Fatal("complete window refused")
		}
		if len(kinds) != 1 || kinds[0] != "summary" {
			t.Fatalf("seal delivered %v", kinds)
		}
	})
}

func TestKeeperSealTimeoutOmitsSummaryAndLeavesCaptureOutsideScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		defer once.Do(func() { close(release) })
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error {
			close(entered)
			<-release
			return nil
		})
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "OWNER", Run: "timeout-run"})
		trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
		<-entered
		trace.emit("watch_reset", time.Time{}, time.Now().Add(time.Minute), "", 1)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		called := false
		if diagnostics.Seal(ctx, func(context.Context, Event) error { called = true; return nil }) {
			t.Fatal("stalled delivery reported a complete seal")
		}
		sequence, lost := trace.sequence, trace.lost.Load()
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		if trace.sequence != sequence || trace.lost.Load() != lost || trace.renewal != 1 {
			t.Fatal("post-cutoff event changed the closed observation window or dropped keeper state")
		}
		once.Do(func() { close(release) })
		synctest.Wait()
		// safety: the queued record and the unwritten summary both count as lost.
		if called || trace.lost.Load() != lost+2 {
			t.Fatalf("expired summary delivered=%v lost=%d want=%d", called, trace.lost.Load(), lost+2)
		}
	})
}

func TestKeeperUndeliverableSealSummaryCountsLossWithoutRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "summary-failure"})
		trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		calls := 0
		sink := func(context.Context, Event) error { calls++; return errors.New("detached bound writer") }
		if diagnostics.Seal(ctx, sink) || trace.lost.Load() != 1 || calls != 1 {
			t.Fatalf("failed summary: lost=%d calls=%d", trace.lost.Load(), calls)
		}
		if diagnostics.Seal(ctx, sink) || calls != 1 {
			t.Fatal("seal was retried")
		}
	})
}

func TestKeeperSealSummarySeparatesInstancesAndFreezesCutoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
		defer diagnostics.Close()
		var traces []*keeperTrace
		for _, role := range []string{"OWNER", "NODE"} {
			trace := diagnostics.start(t.Context(), Identity{Role: role, Run: "pair"})
			trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
			traces = append(traces, trace)
		}
		synctest.Wait()
		diagnostics.Cutoff()
		time.Sleep(time.Second)
		for _, trace := range traces {
			trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		}
		summaries := map[string]Event{}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if !diagnostics.Seal(ctx, func(_ context.Context, event Event) error {
			summaries[event.Identity.Role] = event
			return nil
		}) {
			t.Fatal("complete pair refused")
		}
		if len(summaries) != 2 || summaries["OWNER"].Instance == summaries["NODE"].Instance {
			t.Fatalf("instances conflated: %+v", summaries)
		}
		for _, event := range summaries {
			summary := event.Summary
			if summary == nil || !summary.AsOfSeal || !summary.DrainComplete || summary.Captured != 1 || summary.LastDeliveredSequence != 1 || summary.Lost != 0 || summary.OpenMonoAfter > summary.SealMonoBefore || summary.SealMonoBefore > summary.SealMonoAfter || summary.SealMonoAfter >= event.MonoBefore {
				t.Fatalf("summary/cutoff invalid: %+v", event)
			}
		}
	})
}

func TestKeeperDiagnosticsReopenForTheNextBoundStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stream atomic.Pointer[[]Event]
		first, second := []Event{}, []Event{}
		stream.Store(&first)
		sink := func(_ context.Context, event Event) error {
			records := stream.Load()
			*records = append(*records, event)
			return nil
		}
		diagnostics := NewDiagnostics(t.Context(), sink)
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "retry"})
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		synctest.Wait()
		diagnostics.Cutoff()
		trace.emit("renewal_finish", time.Time{}, time.Time{}, "success", 0)
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if !diagnostics.Seal(ctx, sink) {
			t.Fatal("first window refused")
		}
		time.Sleep(time.Second)
		stream.Store(&second)
		diagnostics.Open()
		trace.emit("renewal_finish", time.Time{}, time.Time{}, "success", 0)
		synctest.Wait()
		next, cancelNext := context.WithTimeout(t.Context(), time.Second)
		defer cancelNext()
		if !diagnostics.Seal(next, sink) {
			t.Fatal("second window refused")
		}
		if len(first) != 2 || first[0].Sequence != 1 || first[1].Summary == nil || first[1].Summary.Captured != 1 {
			t.Fatalf("first window: %+v", first)
		}
		if len(second) != 2 || second[0].Sequence != 2 || second[0].Renewal != 2 || second[0].InFlight || second[1].Summary == nil {
			t.Fatalf("second window lost keeper state or sequence: %+v", second)
		}
		opened, sealed := second[1].Summary, first[1].Summary
		if opened.LastDeliveredSequence != 2 || opened.Captured != 2 || !opened.DrainComplete || opened.OpenMonoBefore <= sealed.SealMonoAfter {
			t.Fatalf("second window summary: %+v after %+v", opened, sealed)
		}
	})
}

func TestKeeperDiagnosticsOpenWaitsForTheSealingWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, event Event) error {
			events = append(events, event)
			return nil
		})
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "reopen"})
		trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		result := make(chan bool)
		go func() {
			result <- diagnostics.Seal(ctx, func(context.Context, Event) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		diagnostics.Open()
		trace.emit("watch_reset", time.Time{}, time.Now().Add(time.Minute), "", 1)
		if trace.sequence != 1 {
			t.Fatal("capture reopened while the previous window was still sealing")
		}
		close(release)
		if !<-result {
			t.Fatal("seal refused")
		}
		trace.emit("watch_reset", time.Time{}, time.Now().Add(time.Minute), "", 1)
		synctest.Wait()
		if trace.sequence != 2 || len(events) != 2 || events[1].Sequence != 2 {
			t.Fatalf("deferred open did not resume capture: sequence=%d events=%+v", trace.sequence, events)
		}
	})
}

func TestKeeperRenewsWhileSealIsStalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
		defer diagnostics.Close()
		var renewals atomic.Int64
		keeper := Keeper{
			Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Identity: Identity{Role: "OWNER", Run: "stall"},
			Renew: func(context.Context) (Beat, error) { renewals.Add(1); return Beat{}, nil },
		}
		unit, stop := keeper.Run(t.Context())
		defer stop()
		synctest.Wait()
		stalled := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan bool)
		go func() {
			result <- diagnostics.Seal(ctx, func(ctx context.Context, event Event) error {
				if event.Summary != nil {
					close(stalled)
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})
		}()
		<-stalled
		before := renewals.Load()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if unit.Err() != nil || renewals.Load() < before+4 {
			t.Fatalf("stalled seal delayed renewal: %d -> %d (%v)", before, renewals.Load(), unit.Err())
		}
		cancel()
		if <-result {
			t.Fatal("stalled summary reported complete")
		}
	})
}
