package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var accountingRetryBodies atomic.Int32

type accountingRetryPipeline struct {
	sparkwing.Base
	failFirst bool
}

func (pipeline accountingRetryPipeline) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(p, "build", func(context.Context) error {
		n := accountingRetryBodies.Add(1)
		if pipeline.failFirst && n == 1 {
			return errors.New("body failed")
		}
		return nil
	}).Retry(1, sparkwing.RetryAuto())
	return nil
}

func init() {
	sparkwing.Register("accounting-unrecorded-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{failFirst: true} })
	sparkwing.Register("accounting-delivery-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{} })
}

type rejectFirstMetric struct {
	localState
	calls  atomic.Int32
	reject bool
}

func (s *rejectFirstMetric) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if s.calls.Add(1) == 1 && s.reject {
		return errors.New("metric delivery rejected")
	}
	return s.localState.AddNodeMetricSample(ctx, run, node, sample)
}

func TestMetricDeliveryFailureUsesAutomaticRetry(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "retry"}[reject], func(t *testing.T) {
			accountingRetryBodies.Store(0)
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			backends := LocalBackends(paths, st, nil)
			metrics := &rejectFirstMetric{localState: localState{st: st}, reject: reject}
			backends.State = metrics
			ctx := withProcessNode(t.Context(), "delivery-retry", "build")
			result, err := Run(ctx, backends, Options{RunID: "delivery-retry", Pipeline: "accounting-delivery-retry"})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("run=%+v error=%v", result, err)
			}
			wantAttempts := int32(1)
			if reject {
				wantAttempts = 2
			}
			if accountingRetryBodies.Load() != wantAttempts || metrics.calls.Load() != wantAttempts {
				t.Fatalf("body executions=%d metric deliveries=%d; want matching execution and delivery counts", accountingRetryBodies.Load(), metrics.calls.Load())
			}
			node, err := st.GetNode(t.Context(), result.RunID, "build")
			if err != nil || node == nil || node.Outcome != "success" || len(node.ExecutionAttempts) != int(wantAttempts) {
				t.Fatalf("retried node=%+v error=%v", node, err)
			}
			profiles, err := st.ListPipelineProfiles(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if !reject && len(profiles) != 2 {
				t.Fatalf("complete execution profile count=%d, want node and run", len(profiles))
			}
			for _, profile := range profiles {
				if !reject && (profile.SampleCount != 1 || !profile.CPUMeasured) {
					t.Fatalf("complete execution did not learn: %+v", profile)
				}
				if reject && profile.SampleCount != 0 {
					t.Errorf("partial retry learned profile: %+v", profile)
				}
			}
		})
	}
}

type rejectFirstExecutionStart struct {
	localState
	calls  atomic.Int32
	reject bool
}

func (s *rejectFirstExecutionStart) AcknowledgeNodeExecutionStart(ctx context.Context, run, node string, start store.ExecutionStart) error {
	if s.calls.Add(1) == 1 && s.reject {
		return errors.New("execution acknowledgement rejected")
	}
	return s.localState.AcknowledgeNodeExecutionStart(ctx, run, node, start)
}

func TestExecutionAcknowledgementFailureExcludesUnrecordedRetry(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "retry"}[reject], func(t *testing.T) {
			accountingRetryBodies.Store(0)
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			backends := LocalBackends(paths, st, nil)
			starts := &rejectFirstExecutionStart{localState: localState{st: st}, reject: reject}
			backends.State = starts
			ctx := withProcessNode(t.Context(), "acknowledgement-retry", "build")
			pipeline := "accounting-delivery-retry"
			if reject {
				pipeline = "accounting-unrecorded-retry"
			}
			result, err := Run(ctx, backends, Options{RunID: "acknowledgement-retry", Pipeline: pipeline})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("run=%+v error=%v", result, err)
			}
			wantCalls := int32(1)
			if reject {
				wantCalls = 2
			}
			if starts.calls.Load() != wantCalls || accountingRetryBodies.Load() != wantCalls {
				t.Fatalf("acknowledgements=%d bodies=%d; want acknowledgements=%d bodies=%d", starts.calls.Load(), accountingRetryBodies.Load(), wantCalls, wantCalls)
			}
			node, err := st.GetNode(t.Context(), result.RunID, "build")
			if err != nil || node == nil || len(node.ExecutionAttempts) != 1 {
				t.Fatalf("node=%+v error=%v; want one recorded body execution", node, err)
			}
			profiles, err := st.ListPipelineProfiles(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if !reject && len(profiles) != 2 {
				t.Fatalf("profiles=%d, want node and run", len(profiles))
			}
			for _, profile := range profiles {
				if !reject && (profile.SampleCount != 1 || !profile.CPUMeasured) {
					t.Errorf("completed body not learned once: %+v", profile)
				}
				if reject && profile.SampleCount != 0 {
					t.Errorf("unrecorded retry learned profile: %+v", profile)
				}
			}
		})
	}
}
