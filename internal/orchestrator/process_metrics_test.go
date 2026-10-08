package orchestrator

import (
	"context"
	"encoding/json"
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

func TestSpawnAccountingFailureAllowsChildAndMarksParent(t *testing.T) {
	for _, failure := range []error{errors.New("parent marker rejected"), store.ErrNodeMetricLimit} {
		t.Run(failure.Error(), func(t *testing.T) {
			st, handler := nodeSpawnFixture(t, "spawn-metric-failure", nil)
			metrics := &rejectParentMarker{localState: localState{st: st}, failure: failure}
			handler.backends.State = metrics
			backends := handler.backends
			bodies := 0
			node := sparkwing.Job(sparkwing.NewPlan(), "parent", func(ctx context.Context) error {
				bodies++
				for range 100 {
					if _, err := sparkwing.Exec(ctx, "true").Run(); err != nil { //nolint:contextcheck // safety: Exec stores ctx; Run takes no context argument.
						return err
					}
				}
				_, err := handler.Spawn(ctx, "parent", "child", nodeSpawnOKChild{})
				return err
			}).Retry(1, sparkwing.RetryAuto())
			ctx := t.Context()
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			result := NewNodeExecutor(backends).RunNode(ctx, runner.Request{RunID: "spawn-metric-failure", NodeID: "parent", Node: node})
			if result.Err != nil || !result.Outcome.OK() || bodies != 1 {
				t.Fatalf("parent=%+v bodies=%d", result, bodies)
			}
			child, err := st.GetNode(ctx, "spawn-metric-failure", "parent/child")
			if err != nil || child.Outcome != "success" {
				t.Fatalf("child=%+v err=%v", child, err)
			}
			samples, err := st.ListNodeMetrics(ctx, "spawn-metric-failure", "parent")
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, sample := range samples {
				unknown = unknown || sample.Kind == store.MetricUnknown
			}
			if metrics.markers != 3 {
				t.Fatalf("parent marker attempts=%d; want initial, spawn, and finish", metrics.markers)
			}
			if !unknown {
				t.Fatalf("parent samples=%+v; want unknown", samples)
			}

			events, err := st.ListEventsAfter(ctx, "spawn-metric-failure", 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			losses := 0
			for _, event := range events {
				if event.Kind != "resource_samples_incomplete" && event.Kind != "metrics_partial" {
					continue
				}
				losses++
				var payload struct{ Lost, Attempted int64 }
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Lost != 0 || payload.Attempted != 100 {
					t.Fatalf("marker changed delivery counts: %s", event.Payload)
				}
			}
			if losses != 1 {
				t.Fatalf("marker loss events=%d; want 1", losses)
			}
		})
	}
}

func TestCommandSampleLossMarksNodeWithoutRetry(t *testing.T) {
	st, backends := metricExecutionFixture(t)
	backends.State = &rejectCommandMetric{localState: localState{st: st}, reject: true}
	ctx := withProcessNode(t.Context(), "run", "build")
	t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
	bodies := 0
	node := sparkwing.Job(sparkwing.NewPlan(), "build", func(ctx context.Context) error {
		bodies++
		_, err := sparkwing.Exec(ctx, "true").Run() //nolint:contextcheck // safety: Exec stores ctx; Run takes no context argument.
		return err
	}).Retry(1, sparkwing.RetryAuto())
	result := NewNodeExecutor(backends).RunNode(ctx, runner.Request{RunID: "run", NodeID: "build", Node: node})
	if result.Err != nil || !result.Outcome.OK() || bodies != 1 {
		t.Fatalf("node=%+v bodies=%d", result, bodies)
	}
	samples, err := st.ListNodeMetrics(ctx, "run", "build")
	if err != nil {
		t.Fatal(err)
	}
	partial := false
	for _, sample := range samples {
		partial = partial || sample.Kind == store.MetricPartial
	}
	if !partial {
		t.Fatalf("samples=%+v; want partial", samples)
	}
}

func TestProcessNodeAccountingDeliveryFailureAllowsSuccess(t *testing.T) {
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
		if !res.Outcome.OK() || res.Err != nil || !bodyRan {
			t.Fatalf("owned=%t execution=%+v body ran=%t", owned, res, bodyRan)
		}
		stored, err := st.GetNode(ctx, "run", "build")
		if err != nil || stored.Outcome != string(sparkwing.Success) {
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

type rejectParentMarker struct {
	localState
	markers int
	failure error
}

func (s *rejectParentMarker) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if node == "parent" && sample.Kind == store.MetricUnknown {
		s.markers++
		if s.markers <= 2 {
			return s.failure
		}
	}
	return s.localState.AddNodeMetricSample(ctx, run, node, sample)
}
