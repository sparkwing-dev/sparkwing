package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: fails with ctx's error once ctx ends, as the attempt executor does when its session is killed.
func hang(ctx context.Context, _ *NodeRun) Result {
	<-ctx.Done()
	return Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
}

func TestTimeoutFailsWithTheExecutorsReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mods   nodeproto.PlanModifiers
		after  time.Duration
		reason string
		msg    string
	}{
		{
			"wall",
			nodeproto.PlanModifiers{TimeoutMS: 5000},
			5 * time.Second, store.FailureTimeout,
			"timeout exceeded (5s): context deadline exceeded",
		},
		{
			"no progress",
			nodeproto.PlanModifiers{NoProgressTimeoutMS: 2000},
			2 * time.Second, store.FailureNoProgressTimeout,
			"no progress for 2s: context deadline exceeded",
		},
		{
			"silence before the wall",
			nodeproto.PlanModifiers{TimeoutMS: 9000, NoProgressTimeoutMS: 2000},
			2 * time.Second, store.FailureNoProgressTimeout,
			"no progress for 2s: context deadline exceeded",
		},
	} {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			res := timeoutLayer(nil, hang)(t.Context(), retryNode(tc.mods))
			if res.Outcome != sparkwing.Failed || res.Reason != tc.reason || res.Err.Error() != tc.msg {
				t.Errorf("%s: outcome %s reason %q err %v, want failed %q %q", tc.name, res.Outcome, res.Reason, res.Err, tc.reason, tc.msg)
			}
			if !errors.Is(res.Err, context.DeadlineExceeded) || time.Since(start) != tc.after {
				t.Errorf("%s: err %v after %s, want a deadline after %s", tc.name, res.Err, time.Since(start), tc.after)
			}
		})
	}
}

func TestTimeoutProgressRestartsTheSilenceCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		chatty := func(ctx context.Context, n *NodeRun) Result {
			for range 3 {
				time.Sleep(1500 * time.Millisecond)
				n.Progress()
			}
			return hang(ctx, n)
		}
		res := timeoutLayer(nil, chatty)(t.Context(), retryNode(nodeproto.PlanModifiers{NoProgressTimeoutMS: 2000}))
		if res.Reason != store.FailureNoProgressTimeout || time.Since(start) != 6500*time.Millisecond {
			t.Errorf("reason %q after %s, want no_progress_timeout 2s after the last line at 4.5s", res.Reason, time.Since(start))
		}
	})
}

func TestTimeoutLeavesOtherEndingsAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mods := nodeproto.PlanModifiers{TimeoutMS: 5000, NoProgressTimeoutMS: 5000}
		late := func(ctx context.Context, _ *NodeRun) Result {
			<-ctx.Done()
			return Result{Outcome: sparkwing.Success}
		}
		if res := timeoutLayer(nil, late)(t.Context(), retryNode(mods)); res.Outcome != sparkwing.Success {
			t.Errorf("a success that outlived its timer became %s", res.Outcome)
		}
		broke := func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("broke"), Reason: store.FailureVerify}
		}
		if res := timeoutLayer(nil, broke)(t.Context(), retryNode(mods)); res.Reason != store.FailureVerify || res.Err.Error() != "broke" {
			t.Errorf("a failure before the timer became %q %v", res.Reason, res.Err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		go func() { time.Sleep(time.Second); cancel() }()
		if res := timeoutLayer(nil, hang)(ctx, retryNode(mods)); res.Reason != "" || !errors.Is(res.Err, context.Canceled) {
			t.Errorf("a run cancel became %q %v", res.Reason, res.Err)
		}
	})
}

func TestTimeoutAppliesToEachRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		calls := 0
		stuck := func(ctx context.Context, n *NodeRun) Result {
			calls++
			return hang(ctx, n)
		}
		mods := nodeproto.PlanModifiers{Retry: 2, RetryBackoffMS: 1000, TimeoutMS: 3000}
		res := retryLayer(nil, timeoutLayer(nil, stuck))(t.Context(), retryNode(mods))
		if calls != 3 || res.Reason != store.FailureTimeout {
			t.Errorf("calls %d reason %q, want 3 timed-out attempts", calls, res.Reason)
		}
		if got, want := time.Since(start), 3*3*time.Second+time.Second+2*time.Second; got != want {
			t.Errorf("took %s, want %s: three full timeouts and the backoffs between", got, want)
		}
	})
}

func TestLogSinkReportsProgressPerRecord(t *testing.T) {
	ticks := 0
	sink, err := NewLogSink(t.Context(), LogSinkConfig{
		RunID: testRunID, NodeID: "build", Log: discardLog{}, State: noState{}, Masker: secrets.NewMasker(),
		Progress: func() { ticks++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	sink.Text(LevelInfo, "compiling")
	sink.Record(nodeproto.LogRecord{Level: "info", Msg: "linking"})
	if ticks != 2 {
		t.Errorf("progress ticks %d, want one per stored record", ticks)
	}
}

type discardLog struct{}

func (discardLog) Emit(sparkwing.LogRecord) {}

type noState struct{}

func (noState) AppendNodeAnnotation(context.Context, string, string, string) error { return nil }

func (noState) AppendStepAnnotation(context.Context, string, string, string, string) error {
	return nil
}

func (noState) SetNodeSummary(context.Context, string, string, string) error { return nil }

func (noState) SetStepSummary(context.Context, string, string, string, string) error { return nil }
