//go:build unix

package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepsHost struct {
	logs *memLogs
	res  RunResult
}

type stepWindowState struct {
	storage.StateStoreOld
	states   map[string]string
	startErr error
}

func (s *stepWindowState) StartNodeStep(_ context.Context, _, _, id string) error {
	if s.startErr != nil {
		return s.startErr
	}
	s.states[id] = "running"
	return nil
}

func (s *stepWindowState) SkipNodeStep(_ context.Context, _, _, id string) error {
	s.states[id] = "skipped"
	return nil
}

func (s *stepWindowState) FinishNodeStep(_ context.Context, _, _, id, state string) error {
	s.states[id] = state
	return nil
}

func (h *stepsHost) records(node, event string) map[string]sparkwing.LogRecord {
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	out := map[string]sparkwing.LogRecord{}
	for _, r := range h.logs.records[node] {
		if r.Event == event {
			out[r.Msg] = r
		}
	}
	return out
}

func (h *stepsHost) wantLog(t *testing.T, node string, present, absent []string) {
	t.Helper()
	text := h.logs.text(node)
	for _, s := range present {
		if !strings.Contains(text, s) {
			t.Errorf("%s log lacks %q:\n%s", node, s, text)
		}
	}
	for _, s := range absent {
		if strings.Contains(text, s) {
			t.Errorf("%s log holds %q:\n%s", node, s, text)
		}
	}
}

func (h *stepsHost) wantNode(t *testing.T, node string, outcome sparkwing.Outcome, errHas string) {
	t.Helper()
	got := h.res.Nodes[node]
	if got.Outcome != outcome || (errHas != "" && !strings.Contains(errText(got.Err), errHas)) {
		t.Errorf("%s = %s (%v), want %s carrying %q", node, got.Outcome, got.Err, outcome, errHas)
	}
}

func TestHostStepWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "stepsfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/stepsfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	host := func(t *testing.T, runID, jobID string, edit func(*Config)) *stepsHost {
		t.Helper()
		h := &stepsHost{logs: &memLogs{records: map[string][]sparkwing.LogRecord{}}}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		state := &stepWindowState{states: map[string]string{}}
		cfg := Config{
			Session: SessionConfig{
				Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second,
				Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
				Engine:     nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			},
			Pipeline: "steps", Args: map[string]string{"set": "main"},
			Run: nodeproto.RunContext{RunID: runID, Pipeline: "steps"}, State: state, Logger: slog.New(slog.DiscardHandler),
		}
		if edit != nil {
			edit(&cfg)
		}
		planned, err := PlanDocument(ctx, cfg.Session, PlanRequest{Pipeline: cfg.Pipeline, Args: cfg.Args, Run: cfg.Run, StartAt: cfg.StartAt, StopAt: cfg.StopAt})
		h.res = RunResult{RunID: runID, Status: "failed", Nodes: map[string]Result{}, Err: err}
		if err != nil {
			return h
		}
		run := newRun(&cfg, planned.Doc, planned.Args, nil)
		for _, job := range planned.Doc.Jobs {
			if job.ID != jobID {
				continue
			}
			masker := secrets.NewMasker()
			sink, err := NewLogSink(ctx, LogSinkConfig{RunID: runID, NodeID: job.ID, Log: memLog{logs: h.logs, node: job.ID}, Masker: masker})
			if err != nil {
				t.Fatal(err)
			}
			sessionConfig := cfg.Session
			sessionConfig.Sink, sessionConfig.Masker = sink, masker
			sessionConfig.Binding = &nodeproto.ProcessBinding{RunID: runID, JobID: job.ID, Routes: nodeproto.ProcessRoutes{URL: "http://unused", Bearer: "step-window-fixture"}}
			session, err := startConfiguredProcess(ctx, sessionConfig)
			if err != nil {
				t.Fatal(err)
			}
			h.res.Order = append(h.res.Order, job.ID)
			result := drive(ctx, session, sink, &NodeRun{Run: run, Plan: job, Attempt: 1})
			closeErr := session.Close()
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			h.res.Nodes[job.ID], h.res.Err = result, result.Err
			if result.Outcome == sparkwing.Success {
				h.res.Status = "success"
			}
		}
		return h
	}

	t.Run("Main", func(t *testing.T) {
		h := host(t, "run-steps", "range", func(c *Config) { c.StartAt = "r-b" })

		t.Run("StartAtSkipsUpstreamSteps", func(t *testing.T) {
			h.wantNode(t, "range", sparkwing.Success, "")
			h.wantLog(t, "range", []string{"ran r-b", "ran r-c"}, []string{"ran r-a"})
			if r := h.records("range", "step_skipped")["r-a"]; r.Attrs["reason"] != "upstream of --start-at=r-b" {
				t.Errorf("step_skipped r-a = %+v, want the window's reason", r)
			}
		})
	})

	t.Run("AnUnknownBoundFailsTheRun", func(t *testing.T) {
		h := host(t, "run-badrange", "range", func(c *Config) { c.StopAt = "r-bb" })
		if h.res.Status != "failed" || !strings.Contains(errText(h.res.Err), `--sw-stop-at "r-bb" references unknown step "r-bb"; did you mean "r-b"?`) {
			t.Errorf("status=%s err=%v, want failed naming the closest step", h.res.Status, h.res.Err)
		}
		if len(h.res.Order) != 0 {
			t.Errorf("nodes %v started under an unknown bound", h.res.Order)
		}
	})
	t.Run("ARefusedStepWritePreventsBodiesAndRetries", func(t *testing.T) {
		refused := errors.New("step write refused")
		h := host(t, "run-refused-step", "range", func(c *Config) {
			c.State = &stepWindowState{states: map[string]string{}, startErr: refused}
		})
		result := h.res.Nodes["range"]
		if result.Outcome != sparkwing.Failed || !result.Final || !errors.Is(result.Err, refused) || strings.Contains(h.logs.text("range"), "ran ") {
			t.Fatalf("result=%+v log=%s", result, h.logs.text("range"))
		}
	})
	t.Run("StepPredicatesUseTheSameTerminalSnapshotAsBodies", func(t *testing.T) {
		h := host(t, "run-skips", "skips", nil)
		h.wantNode(t, "skips", sparkwing.Success, "")
		h.wantLog(t, "skips", []string{"ran b"}, []string{"ran a"})
	})
	t.Run("ThrowingPredicateNeverRunsItsBody", func(t *testing.T) {
		h := host(t, "run-guard-failed", "skippanic", nil)
		h.wantNode(t, "skippanic", sparkwing.Failed, "guard blew up")
		h.wantLog(t, "skippanic", nil, []string{"guarded ran"})
	})
}

func TestRangeSkips(t *testing.T) {
	linear := []nodeproto.StepSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"b"}}}
	branching := []nodeproto.StepSpec{{ID: "root"}, {ID: "left", Needs: []string{"root"}}, {ID: "right", Needs: []string{"root"}}, {ID: "join", Needs: []string{"left", "right"}}}
	for _, tc := range []struct {
		name        string
		steps       []nodeproto.StepSpec
		start, stop string
		want        map[string]string
	}{
		{"StartAt", linear, "b", "", map[string]string{"a": "upstream of --start-at=b"}},
		{"StopAt", linear, "", "b", map[string]string{"c": "downstream of --stop-at=b"}},
		{"StartEqualsStop", linear, "b", "b", map[string]string{"a": "outside --start-at=b --stop-at=b range", "c": "outside --start-at=b --stop-at=b range"}},
		{"BranchingStartAtSkipsAllButItsDescendants", branching, "left", "", map[string]string{"root": "upstream of --start-at=left", "right": "upstream of --start-at=left"}},
		{"NoBounds", linear, "", "", map[string]string{}},
		{"BoundInAnotherNode", linear, "elsewhere", "", map[string]string{}},
		{"OneBoundHere", linear, "elsewhere", "b", map[string]string{"c": "outside --start-at=elsewhere --stop-at=b range"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			children := map[string][]string{}
			for _, st := range tc.steps {
				for _, d := range st.Needs {
					children[d] = append(children[d], st.ID)
				}
			}
			got := rangeSkips(tc.steps, children, tc.start, tc.stop)
			if got == nil {
				got = map[string]string{}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("skips = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateStepsRefusesAnUnorderableNode(t *testing.T) {
	for name, steps := range map[string][]nodeproto.StepSpec{
		"duplicate":    {{ID: "a"}, {ID: "a"}},
		"unknown need": {{ID: "a", Needs: []string{"ghost"}}},
		"cycle":        {{ID: "a", Needs: []string{"b"}}, {ID: "b", Needs: []string{"a"}}},
		"no id":        {{ID: ""}},
	} {
		err := validateNodes([]nodeproto.JobSpec{{ID: "n", Steps: steps}}, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}})
		if perr := (*apierr.Error)(nil); !errors.As(err, &perr) || perr.Code != apierr.PlanInvalid {
			t.Errorf("%s: err = %v, want plan_invalid", name, err)
		}
	}
	ok := []nodeproto.StepSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"a", "b"}}}
	if err := validateNodes([]nodeproto.JobSpec{{ID: "n", Steps: ok}}, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err != nil {
		t.Errorf("a valid step graph: %v", err)
	}
}
