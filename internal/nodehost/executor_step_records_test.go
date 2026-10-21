package nodehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type executorStepStore struct {
	NodeTransitionStore
	mu      sync.Mutex
	reports []store.StepReport
	write   func(context.Context, store.StepReport) error
}

func TestNodeExecutorUsesSealedStepWindow(t *testing.T) {
	for _, tc := range []struct {
		start, stop string
		want        []string
	}{
		{"second", "", []string{"second", "third"}},
		{"", "second", []string{"first", "second"}},
		{"second", "second", []string{"second"}},
		{"other-job-step", "", []string{"first", "second", "third"}},
	} {
		t.Run(tc.start+"/"+tc.stop, func(t *testing.T) {
			input, state, deps, processes := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps = []nodeproto.StepSpec{{ID: "first"}, {ID: "second", Needs: []string{"first"}}, {ID: "third", Needs: []string{"second"}}}
			executorChangeJob(t, &input, state, job)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Resolved.Settings.Options.StartAt = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: tc.start != "", Value: tc.start}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}
			spec.Resolved.Settings.Options.StopAt = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: tc.stop != "", Value: tc.stop}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}
			spec.Request.Request.Options = spec.Resolved.Settings.Options
			var err error
			input.Accepted.Spec, err = json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			records := &executorStepStore{NodeTransitionStore: state}
			deps.Store = records
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, activity)
				process.(*executorProcess).plan = input.Accepted.Plan
				return process, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			var ran []string
			for _, request := range (*processes)[0].requests {
				if request.Op == nodeproto.OpRunStep {
					ran = append(ran, request.Step)
				}
			}
			if !reflect.DeepEqual(ran, tc.want) {
				t.Fatalf("ran=%v, want=%v", ran, tc.want)
			}
			states := map[string]string{}
			for _, report := range records.reports {
				states[report.StepID] = report.State
			}
			for _, step := range job.Steps {
				want := "skipped"
				for _, id := range tc.want {
					if step.ID == id {
						want = "passed"
					}
				}
				if states[step.ID] != want {
					t.Errorf("%s=%s, want=%s", step.ID, states[step.ID], want)
				}
			}
		})
	}
}

func FuzzExecutorStepRecordFenceAndMask(f *testing.F) {
	f.Add("holder:other", "private\"\nvalue", uint8(4))
	f.Add("", "x", uint8(0))
	f.Fuzz(func(t *testing.T, identity, secret string, ordinal uint8) {
		secret = "step-secret:" + secret
		forms := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), url.QueryEscape(secret)}
		encoded, err := json.Marshal(secret)
		if err != nil {
			t.Fatal(err)
		}
		forms = append(forms, string(encoded[1:len(encoded)-1]))
		input, st, deps, _ := executorFixture(t)
		input.Authority.Holder, st.row.ClaimedBy = "holder:"+identity, "holder:"+identity
		input.Authority.CapabilityPrefix, st.row.CapabilityPrefix = "cap:"+identity, "cap:"+identity
		input.Generation, st.row.Generation = runspec.ClaimGeneration(ordinal)+1, runspec.ClaimGeneration(ordinal)+1
		input.Attempt, st.row.Attempt = runspec.AttemptOrdinal(int64(ordinal)), runspec.AttemptOrdinal(int64(ordinal))
		masker := secrets.NewMasker()
		masker.Register(secret)
		deps.Mask = masker.Mask
		var diagnostics []nodeproto.FrameLog
		deps.Log = func(_ context.Context, _ NodeInput, log nodeproto.FrameLog) error {
			diagnostics = append(diagnostics, log)
			var attrs any
			if err := json.Unmarshal(log.Attrs, &attrs); err != nil {
				t.Fatal(err)
			}
			decoded, err := json.Marshal(attrs)
			if err != nil {
				t.Fatal(err)
			}
			for _, form := range forms {
				if strings.Contains(log.Message, form) || strings.Contains(string(log.Attrs), form) || strings.Contains(string(decoded), form) {
					t.Fatal("diagnostic exposed a registered failure")
				}
			}
			return nil
		}
		records := &executorStepStore{NodeTransitionStore: st}
		deps.Store = records
		start := deps.Start
		deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			p, err := start(ctx, in, activity)
			p.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "failure:" + forms[int(ordinal)%len(forms)]}}
			return p, err
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.Execute(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		if len(records.reports) != 2 {
			t.Fatalf("step reports=%d", len(records.reports))
		}
		if len(diagnostics) < 2 {
			t.Fatal("step diagnostics are absent")
		}
		for _, report := range records.reports {
			if report.Actor != executorActor(input) || runspec.AttemptOrdinal(report.Attempt.Attempt) != input.Attempt+1 || runspec.ClaimGeneration(report.Attempt.Generation) != input.Generation || report.Attempt.RunID != input.Accepted.RunID || report.Attempt.NodeID != input.NodeID {
				t.Fatal("step changed its awarded fence")
			}
			for _, form := range forms {
				if strings.Contains(report.MaskedError, form) {
					t.Fatal("step persisted an unmasked failure")
				}
			}
		}
	})
}

func (s *executorStepStore) RecordStep(ctx context.Context, report store.StepReport) error {
	s.mu.Lock()
	s.reports = append(s.reports, report)
	s.mu.Unlock()
	if s.write != nil {
		return s.write(ctx, report)
	}
	return nil
}

func TestNodeExecutorPersistsStepStatesAndFences(t *testing.T) {
	for _, mode := range []string{"success", "skipped", "failed", "optional", "retry", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			input, st, deps, _ := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps = []nodeproto.StepSpec{{ID: "first"}, {ID: "second", Needs: []string{"first"}}}
			job.FailurePolicy = "collect_all"
			job.Steps[0].Optional = mode == "optional"
			if mode == "skipped" {
				job.Steps[0].Closures.SkipIf = []string{"guard"}
			}
			executorChangeJob(t, &input, st, job)
			if mode == "retry" {
				executorRetryBudget(t, &input, st, 1)
			}
			if mode == "dry_run" {
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				spec.Resolved.Settings.Options.DryRun.Value = true
				input.Accepted.Spec, _ = json.Marshal(spec)
			}
			records := &executorStepStore{NodeTransitionStore: st}
			deps.Store = records
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, in, activity)
				p.(*executorProcess).plan = input.Accepted.Plan
				p.(*executorProcess).answer = func(_ context.Context, r nodeproto.Request) (any, error) {
					if r.Op == nodeproto.OpEval {
						return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
					}
					if r.Op != nodeproto.OpRunStep {
						return nil, nil
					}
					if mode == "dry_run" || mode == "skipped" && r.Step == "first" {
						t.Error("skipped step body ran")
					}
					records.mu.Lock()
					defer records.mu.Unlock()
					started := false
					for _, report := range records.reports {
						started = started || report.StepID == r.Step && report.State == "running" && runspec.AttemptOrdinal(report.Attempt.Attempt) == in.Attempt
					}
					if !started {
						t.Error("body ran before its running record committed")
					}
					if r.Step == "first" && (mode == "failed" || mode == "optional" || mode == "retry" && in.Attempt == input.Attempt+1) {
						return nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "private failure"}}, nil
					}
					return nodeproto.RunResult{Outcome: "success"}, nil
				}
				return p, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			states := map[int64]map[string][]string{}
			for _, report := range records.reports {
				if report.Attempt.RunID != input.Accepted.RunID || report.Attempt.NodeID != input.NodeID || runspec.ClaimGeneration(report.Attempt.Generation) != input.Generation || report.Actor.Holder != input.Authority.Holder || report.Actor.Generation != input.Generation || report.Actor.CapabilityPrefix != input.Authority.CapabilityPrefix || report.Actor.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}) {
					t.Fatalf("step lost its attempt fence: %+v", report)
				}
				if strings.Contains(report.MaskedError, "private") || report.State == "running" && report.StartedAt == nil || report.State != "running" && report.FinishedAt == nil {
					t.Fatalf("step lost masking or timestamps: %+v", report)
				}
				if states[report.Attempt.Attempt] == nil {
					states[report.Attempt.Attempt] = map[string][]string{}
				}
				states[report.Attempt.Attempt][report.StepID] = append(states[report.Attempt.Attempt][report.StepID], report.State)
			}
			want := map[string][]string{"first": {"running", "passed"}, "second": {"running", "passed"}}
			if mode == "failed" || mode == "optional" || mode == "retry" {
				want = map[string][]string{"first": {"running", "failed"}, "second": {"cancelled"}}
			} else if mode == "skipped" {
				want["first"] = []string{"running", "skipped"}
			} else if mode == "dry_run" {
				want["first"], want["second"] = []string{"running", "skipped"}, []string{"running", "skipped"}
			}
			if !reflect.DeepEqual(states[int64(input.Attempt)+1], want) {
				t.Fatalf("step states=%v, want=%v", states, want)
			}
			if mode == "retry" && !reflect.DeepEqual(states[int64(input.Attempt)+2], map[string][]string{"first": {"running", "passed"}, "second": {"running", "passed"}}) {
				t.Fatalf("retry reused previous step rows: %v", states)
			}
		})
	}
}

func TestNodeExecutorStepWriteRefusalPreventsSuccessAndJoinsWork(t *testing.T) {
	for _, state := range []string{"running", "passed", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			input, st, deps, processes := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps = []nodeproto.StepSpec{{ID: "first", Optional: true}, {ID: "second", Needs: []string{"first"}}}
			job.FailurePolicy = "collect_all"
			executorChangeJob(t, &input, st, job)
			executorRetryBudget(t, &input, st, 2)
			refused := errors.New("step persistence refused")
			records := &executorStepStore{NodeTransitionStore: st, write: func(_ context.Context, report store.StepReport) error {
				if report.State == state {
					return refused
				}
				return nil
			}}
			deps.Store = records
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, in, activity)
				p.(*executorProcess).plan = input.Accepted.Plan
				if state == "cancelled" || state == "failed" {
					p.(*executorProcess).result = nodeproto.RunResult{Outcome: "failed"}
				}
				return p, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(ctx, input)
			if !errors.Is(err, refused) || st.changes[len(st.changes)-1].To != store.NodeFailed {
				t.Fatalf("step refusal was hidden: changes=%+v error=%v", st.changes, err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("step refusal retried or leaked the SDK process")
			}
			if state == "running" {
				for _, r := range (*processes)[0].requests {
					if r.Op == nodeproto.OpRunStep {
						t.Fatal("refused running record reached a body")
					}
				}
			}
		})
	}
}

func TestNodeExecutorStepWriteRefusalJoinsParallelBodies(t *testing.T) {
	for _, sink := range []string{"state", "diagnostic", "window"} {
		t.Run(sink, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			input, st, deps, processes := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps = []nodeproto.StepSpec{{ID: "slow"}, {ID: "refuse"}, {ID: "cleanup", Needs: []string{"slow", "refuse"}, Finally: true}}
			executorChangeJob(t, &input, st, job)
			if sink == "window" {
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				spec.Resolved.Settings.Options.StopAt = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "slow"}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}
				spec.Request.Request.Options.StopAt = spec.Resolved.Settings.Options.StopAt
				var err error
				input.Accepted.Spec, err = json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
			}
			entered, joined := make(chan struct{}), make(chan struct{})
			refused := errors.New("terminal step write refused")
			records := &executorStepStore{NodeTransitionStore: st, write: func(ctx context.Context, report store.StepReport) error {
				if report.StepID != "refuse" {
					return nil
				}
				if report.State == "passed" && sink == "state" {
					return refused
				}
				select {
				case <-entered:
					if report.State == "skipped" && sink == "window" {
						return refused
					}
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}}
			deps.Store = records
			if sink == "diagnostic" {
				deps.Log = func(_ context.Context, _ NodeInput, frame nodeproto.FrameLog) error {
					if frame.Step == "refuse" && frame.Event == "step_end" {
						return refused
					}
					return nil
				}
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, in, activity)
				p.(*executorProcess).plan = input.Accepted.Plan
				p.(*executorProcess).answer = func(ctx context.Context, r nodeproto.Request) (any, error) {
					if r.Op != nodeproto.OpRunStep {
						return nil, nil
					}
					if r.Step == "cleanup" {
						t.Error("persistence failure started further author code")
					}
					if r.Step == "refuse" && sink == "window" {
						t.Error("excluded step body ran")
					}
					if r.Step == "slow" {
						close(entered)
						defer close(joined)
						<-ctx.Done()
						return nil, context.Cause(ctx)
					}
					return nodeproto.RunResult{Outcome: "success"}, nil
				}
				return p, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(ctx, input); !errors.Is(err, refused) {
				t.Fatalf("error=%v", err)
			}
			select {
			case <-joined:
			default:
				t.Fatal("parallel body outlived the failed execution")
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 || st.changes[len(st.changes)-1].To != store.NodeFailed {
				t.Fatal("step refusal retried or escaped its process lifecycle")
			}
		})
	}
}
