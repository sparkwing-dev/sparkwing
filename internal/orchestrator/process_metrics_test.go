package orchestrator

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type rejectedMetrics struct {
	localState
	err error
}

func (s rejectedMetrics) AddNodeMetricSample(context.Context, string, string, store.MetricSample) error {
	return s.err
}

func TestProcessNodeAccountingRequiresExactOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, run, node string
		owned           bool
	}{
		{"dedicated", "run", "build", true},
		{"embedded", "", "", false},
		{"other run", "other", "build", false},
		{"nested node", "run", "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, backends := metricExecutionFixture(t)
			ctx := t.Context()
			if tc.run != "" {
				ctx = withProcessNode(ctx, tc.run, tc.node)
			}
			bodyRan := false
			node := sparkwing.Job(sparkwing.NewPlan(), "build", func(context.Context) error { bodyRan = true; return nil })
			res := NewNodeExecutor(backends).RunNode(ctx, runner.Request{RunID: "run", NodeID: "build", Node: node})
			if !res.Outcome.OK() || res.Err != nil || !bodyRan {
				t.Fatalf("execution = %+v, body ran=%t", res, bodyRan)
			}
			samples, err := st.ListNodeMetrics(ctx, "run", "build")
			if err != nil || len(samples) == 0 {
				t.Fatalf("metrics = %+v, %v", samples, err)
			}
			for _, sample := range samples {
				measured := tc.owned && (runtime.GOOS == "linux" || runtime.GOOS == "darwin")
				if (sample.Kind == store.MetricInterval) != measured {
					t.Fatalf("owned=%t produced sample %+v", tc.owned, sample)
				}
			}
		})
	}
}

func TestSpawnAccountingFailureStopsChildAndRemainsWithParent(t *testing.T) {
	st, handler := nodeSpawnFixture(t, "spawn-metric-failure", nil)
	failure := errors.New("parent metric rejected")
	handler.backends.State = rejectedMetrics{localState: localState{st: st}, err: failure}
	failures := make(chan error, 1)
	ctx := context.WithValue(t.Context(), metricErrorsKey{}, failures)
	if _, err := handler.Spawn(ctx, "parent", "child", nodeSpawnOKChild{}); !errors.Is(err, failure) {
		t.Fatalf("spawn error=%v; want metric rejection", err)
	}
	child, err := st.GetNode(ctx, "spawn-metric-failure", "parent/child")
	if err != nil || child.StartedAt != nil || child.Outcome != string(sparkwing.Failed) {
		t.Fatalf("child executed or remained pending: %+v, %v", child, err)
	}
	select {
	case err := <-failures:
		if !errors.Is(err, failure) {
			t.Fatalf("parent retained %v", err)
		}
	default:
		t.Fatal("caught spawn error would erase parent's accounting failure")
	}
}

func TestProcessNodeAccountingDeliveryFailurePreventsSuccess(t *testing.T) {
	for _, owned := range []bool{false, true} {
		st, backends := metricExecutionFixture(t)
		failure := errors.New("metric storage unavailable")
		backends.State = rejectedMetrics{localState: localState{st: st}, err: failure}
		ctx := t.Context()
		if owned {
			ctx = withProcessNode(ctx, "run", "build")
		}
		t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
		bodyRan := false
		node := sparkwing.Job(sparkwing.NewPlan(), "build", func(context.Context) error { bodyRan = true; return nil })
		res := NewNodeExecutor(backends).RunNode(ctx, runner.Request{RunID: "run", NodeID: "build", Node: node})
		if res.Outcome != sparkwing.Failed || !errors.Is(res.Err, failure) || bodyRan != owned {
			t.Fatalf("owned=%t execution=%+v body ran=%t", owned, res, bodyRan)
		}
		stored, err := st.GetNode(ctx, "run", "build")
		if err != nil || stored.Outcome != string(sparkwing.Failed) {
			t.Fatalf("stored node=%+v, %v", stored, err)
		}
	}
}

func metricExecutionFixture(t *testing.T) (*store.Store, Backends) {
	t.Helper()
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "metrics", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	return st, LocalBackends(paths, st, nil)
}
