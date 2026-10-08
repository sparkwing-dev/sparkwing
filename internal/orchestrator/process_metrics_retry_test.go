package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type accountingRetryPipeline struct {
	sparkwing.Base
	failFirst bool
	commands  int
}

func (pipeline accountingRetryPipeline) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	var bodies atomic.Int32
	sparkwing.Job(p, "build", func(ctx context.Context) error {
		sparkwing.LoggerFromContext(ctx).Log("info", "accounting body executed")
		n := bodies.Add(1)
		if pipeline.failFirst && n == 1 {
			return errors.New("body failed")
		}
		for range pipeline.commands {
			if _, err := sparkwing.Exec(ctx, "true").Run(); err != nil { //nolint:contextcheck // safety: Exec stores ctx; Run takes no context argument.
				return err
			}
		}
		return nil
	}).Retry(1, sparkwing.RetryAuto())
	return nil
}

func init() {
	sparkwing.Register("accounting-unrecorded-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{failFirst: true} })
	sparkwing.Register("accounting-delivery-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{commands: 2} })
	sparkwing.Register("accounting-delivery-boundary", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{commands: 99} })
	sparkwing.Register("accounting-delivery-partial", func() sparkwing.Pipeline[sparkwing.NoInputs] { return accountingRetryPipeline{commands: 100} })
}

func TestMetricDeliveryFailureDoesNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name, pipeline     string
		reject, incomplete bool
		attempted          int64
	}{
		{"complete", "accounting-delivery-retry", false, false, 3},
		{"incomplete", "accounting-delivery-retry", true, true, 3},
		{"one percent", "accounting-delivery-boundary", true, false, 100},
		{"below one percent", "accounting-delivery-partial", true, false, 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reject := tc.reject
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			backends := LocalBackends(paths, st, nil)
			metrics := &rejectCommandMetric{localState: localState{st: st}, reject: reject}
			backends.State = metrics
			ctx := withProcessNode(t.Context(), "delivery-retry", "build")
			result, err := Run(ctx, backends, Options{RunID: "delivery-retry", Pipeline: tc.pipeline})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("run=%+v error=%v", result, err)
			}
			wantAttempts := int32(1)

			logBytes, err := os.ReadFile(paths.NodeLog(result.RunID, "build"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(logBytes), "accounting body executed"); got != 1 {
				t.Fatalf("body executions=%d; want 1", got)
			}
			if got := strings.Count(string(logBytes), "resource samples incomplete for"); got != map[bool]int{false: 0, true: 1}[tc.incomplete] {
				t.Fatalf("warnings=%d", got)
			}
			samples, err := st.ListNodeMetrics(ctx, result.RunID, "build")
			if err != nil {
				t.Fatal(err)
			}
			unknown, partialMeasurement := false, false
			for _, sample := range samples {
				unknown = unknown || sample.Kind == store.MetricUnknown
				partialMeasurement = partialMeasurement || sample.Kind == store.MetricPartial
			}
			// safety: without a process sampler, the opening and closing readings are unknown samples.
			unmeasured := runtime.GOOS == "windows"
			wantAttempted := tc.attempted
			if unmeasured {
				wantAttempted++
			}
			if unknown != unmeasured || partialMeasurement != tc.incomplete {
				t.Fatalf("unknown=%v partial=%v samples=%+v", unknown, partialMeasurement, samples)
			}
			events, err := st.ListEventsAfter(ctx, result.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			warnings, partial := 0, 0
			for _, event := range events {
				if event.Kind == "metrics_partial" || event.Kind == "resource_samples_incomplete" {
					var payload struct{ Lost, Attempted int64 }
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Lost != 1 || payload.Attempted != wantAttempted {
						t.Fatalf("loss event=%s", event.Payload)
					}
				}
				if event.Kind == "metrics_partial" {
					partial++
				}
				if event.Kind == "resource_samples_incomplete" {
					warnings++
				}
			}
			if warnings != map[bool]int{false: 0, true: 1}[tc.incomplete] {
				t.Fatalf("warning events=%d", warnings)
			}

			if partial != map[bool]int{false: 0, true: 1}[reject && !tc.incomplete] {
				t.Fatalf("partial events=%d", partial)
			}
			node, err := st.GetNode(t.Context(), result.RunID, "build")
			if err != nil || node == nil || node.Outcome != "success" || len(node.ExecutionAttempts) != int(wantAttempts) {
				t.Fatalf("retried node=%+v error=%v", node, err)
			}
			profiles, err := st.ListPipelineProfiles(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if unmeasured && len(profiles) != 0 {
				t.Fatalf("unmeasured native execution learned profiles: %+v", profiles)
			}
			if !unmeasured && len(profiles) != 2 {
				t.Fatalf("complete execution profile count=%d, want node and run", len(profiles))
			}
			wantCount := map[bool]int{false: 1, true: 0}[tc.incomplete]
			for _, profile := range profiles {
				if profile.SampleCount != wantCount || !profile.CPUMeasured {
					t.Fatalf("execution profile = %+v, want %d clean samples", profile, wantCount)
				}
			}
		})
	}
}

func TestPartialCommandMeasurementsOnlyRaiseProfiles(t *testing.T) {
	for _, raise := range []bool{false, true} {
		t.Run(map[bool]string{false: "lower usage", true: "higher memory"}[raise], func(t *testing.T) {
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			key := currentProfileKey("accounting-delivery-retry")
			memory := int64(1 << 50)
			if raise {
				memory = 1
			}
			seed := store.ProfileObservation{Duration: time.Minute, PeakCores: 1000, SustainedCores: 900, PeakMemoryBytes: memory, CPUMeasured: true}
			if err := st.RecordProfileObservation(t.Context(), key, "build", seed); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetPipelineProfile(t.Context(), key, "build")
			if err != nil {
				t.Fatal(err)
			}
			windowBefore, err := st.ProfileSamples(t.Context(), key, "build")
			if err != nil {
				t.Fatal(err)
			}
			backends := LocalBackends(paths, st, nil)
			backends.State = &rejectCommandMetric{localState: localState{st: st}, reject: true}
			result, err := Run(withProcessNode(t.Context(), "partial-profile", "build"), backends, Options{RunID: "partial-profile", Pipeline: "accounting-delivery-retry"})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("run=%+v error=%v", result, err)
			}
			after, err := st.GetPipelineProfile(t.Context(), key, "build")
			if err != nil {
				t.Fatal(err)
			}
			windowAfter, err := st.ProfileSamples(t.Context(), key, "build")
			if err != nil {
				t.Fatal(err)
			}
			if !raise {
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(windowBefore, windowAfter) {
					t.Fatalf("lower partial changed profile: before=%+v after=%+v windows=%+v / %+v", before, after, windowBefore, windowAfter)
				}
				return
			}
			if after.PeakCores != before.PeakCores || after.SustainedCores != before.SustainedCores || after.PeakMemoryBytes <= before.PeakMemoryBytes || len(windowAfter) != len(windowBefore)+1 {
				t.Fatalf("partial did not raise only memory: before=%+v after=%+v window=%+v", before, after, windowAfter)
			}
			node, err := st.GetNode(t.Context(), result.RunID, "build")
			if err != nil {
				t.Fatal(err)
			}
			last := windowAfter[len(windowAfter)-1]
			if node.StartedAt == nil || node.FinishedAt == nil || last.Duration != time.Duration(node.ProcessWallNanos) {
				t.Fatalf("partial duration=%s node=%+v", last.Duration, node)
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
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			backends := LocalBackends(paths, st, nil)
			starts := &rejectFirstExecutionStart{localState: localState{st: st}, reject: reject}
			backends.State = starts
			ctx := t.Context()
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
			logBytes, err := os.ReadFile(paths.NodeLog(result.RunID, "build"))
			if err != nil {
				t.Fatal(err)
			}
			bodies := strings.Count(string(logBytes), "accounting body executed")
			if starts.calls.Load() != wantCalls || bodies != int(wantCalls) {
				t.Fatalf("acknowledgements=%d bodies=%d; want %d", starts.calls.Load(), bodies, wantCalls)
			}
			node, err := st.GetNode(t.Context(), result.RunID, "build")
			if err != nil || node == nil || len(node.ExecutionAttempts) != 1 {
				t.Fatalf("node=%+v error=%v; want one recorded body execution", node, err)
			}
			profiles, err := st.ListPipelineProfiles(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "windows" && len(profiles) != 0 {
				t.Fatalf("unmeasured native execution learned profiles: %+v", profiles)
			}
			if runtime.GOOS != "windows" && !reject && len(profiles) != 2 {
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

type rejectCommandMetric struct {
	localState
	commands int
	reject   bool
}

func (s *rejectCommandMetric) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if sample.Kind == store.MetricCommand {
		s.commands++
		if s.reject && s.commands == 1 {
			return errors.New("command sample rejected")
		}
	}
	return s.localState.AddNodeMetricSample(ctx, run, node, sample)
}

func (s *rejectCommandMetric) StartNode(ctx context.Context, run, node string) error {
	if err := s.localState.StartNode(ctx, run, node); err != nil {
		return err
	}
	return s.localState.AddNodeUsage(ctx, run, node, store.NodeUsage{CPUTime: time.Second, MaxRSSBytes: 256 << 20, Wall: 2 * time.Second})
}

func (s *rejectFirstExecutionStart) StartNode(ctx context.Context, run, node string) error {
	if err := s.localState.StartNode(ctx, run, node); err != nil {
		return err
	}
	return s.localState.AddNodeUsage(ctx, run, node, store.NodeUsage{CPUTime: time.Second, MaxRSSBytes: 256 << 20, Wall: 2 * time.Second})
}

func (s *rejectFirstExecutionStart) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if !s.reject && sample.Kind == store.MetricUnknown {
		return errors.New("unowned marker rejected")
	}
	return s.localState.AddNodeMetricSample(ctx, run, node, sample)
}

type rejectMetricKind struct {
	localState
	kind     store.MetricKind
	rejected atomic.Int32
	once     bool
}

func (s *rejectMetricKind) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if sample.Kind == s.kind && (!s.once || s.rejected.Load() == 0) {
		s.rejected.Add(1)
		return errors.New("metric kind rejected")
	}
	return s.localState.AddNodeMetricSample(ctx, run, node, sample)
}

func TestLostExclusionMarkersAreRetriedAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend func(localState) *rejectMetricKind
		owned   bool
		warning string
	}{
		{"unowned marker", func(l localState) *rejectMetricKind {
			return &rejectMetricKind{localState: l, kind: store.MetricUnknown, once: true}
		}, false, "the exclusion was recorded at finish"},
		{"partial kind refused", func(l localState) *rejectMetricKind {
			return &rejectMetricKind{localState: l, kind: store.MetricPartial}
		}, true, "the backend refused the partial marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			paths := PathsAt(t.TempDir())
			if err := paths.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			st, err := teststore.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			backends := LocalBackends(paths, st, nil)
			metrics := tc.backend(localState{st: st})
			if metrics.kind == store.MetricPartial {
				backends.State = &rejectCommandThenKind{rejectMetricKind: metrics}
			} else {
				backends.State = metrics
			}
			ctx := t.Context()
			if tc.owned {
				ctx = withProcessNode(ctx, "exclusion", "build")
			}
			result, err := Run(ctx, backends, Options{RunID: "exclusion", Pipeline: "accounting-delivery-retry"})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("run=%+v error=%v", result, err)
			}
			samples, err := st.ListNodeMetrics(ctx, result.RunID, "build")
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, sample := range samples {
				unknown = unknown || sample.Kind == store.MetricUnknown
			}
			if !unknown || metrics.rejected.Load() == 0 {
				t.Fatalf("exclusion not recorded: rejected=%d samples=%+v", metrics.rejected.Load(), samples)
			}
			logBytes, err := os.ReadFile(paths.NodeLog(result.RunID, "build"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(logBytes), tc.warning) != 1 {
				t.Fatalf("node log lacks %q:\n%s", tc.warning, logBytes)
			}
			profiles, err := st.ListPipelineProfiles(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(profiles) != 0 {
				t.Fatalf("excluded measurement was learned: %+v", profiles)
			}
		})
	}
}

type rejectCommandThenKind struct {
	*rejectMetricKind
	commands atomic.Int32
}

func (s *rejectCommandThenKind) AddNodeMetricSample(ctx context.Context, run, node string, sample store.MetricSample) error {
	if sample.Kind == store.MetricCommand && s.commands.Add(1) == 1 {
		return errors.New("command sample rejected")
	}
	return s.rejectMetricKind.AddNodeMetricSample(ctx, run, node, sample)
}
