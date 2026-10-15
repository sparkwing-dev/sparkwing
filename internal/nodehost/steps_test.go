//go:build unix

package nodehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepsHost struct {
	*attemptHarness
	runID string
	res   RunResult
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

func (h *stepsHost) outcomes(t *testing.T, node string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for id, r := range h.records(node, "step_end") {
		got[id] = fmt.Sprint(r.Attrs["outcome"])
	}
	for id := range h.records(node, "step_skipped") {
		got[id] = "skipped"
	}
	return got
}

func (h *stepsHost) rows(t *testing.T, node string) map[string]string {
	t.Helper()
	steps, err := h.st.ListNodeSteps(t.Context(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range steps {
		if s.NodeID == node {
			got[s.StepID] = s.Status
		}
	}
	return got
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

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestHostDrivesSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "stepsfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/stepsfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	host := func(t *testing.T, runID, set string, edit func(*Config)) *stepsHost {
		t.Helper()
		srv, err := Start(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		h := &stepsHost{attemptHarness: &attemptHarness{st: openStore(t), logs: &memLogs{records: map[string][]sparkwing.LogRecord{}}}, runID: runID}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cfg := Config{
			Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
			Pipeline: "steps", Args: map[string]string{"set": set},
			Run:   nodeproto.RunContext{RunID: runID},
			State: h.st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
			Attempts: &Attempts{
				Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
				OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
					return memLog{logs: h.logs, node: node}, nil
				},
			},
		}
		if edit != nil {
			edit(&cfg)
		}
		if h.res, err = Execute(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		return h
	}

	t.Run("Main", func(t *testing.T) {
		h := host(t, "run-steps", "main", func(c *Config) { c.StartAt = "r-b" })

		t.Run("NeedsAndGroupsOrderStepsInOneProcess", func(t *testing.T) {
			h.wantNode(t, "order", sparkwing.Success, "")
			if got := string(h.res.Nodes["order"].Output); got != `"result-from-a"` {
				t.Errorf("order output = %s, want the result step's", got)
			}
			h.wantLog(t, "order", []string{"b saw from-a after 2 checks"}, nil)
			want := map[string]string{"a": "success", "c1": "success", "c2": "success", "b": "success"}
			if got := h.outcomes(t, "order"); !equalMaps(got, want) {
				t.Errorf("order step outcomes = %v, want %v", got, want)
			}
			want = map[string]string{"a": store.StepPassed, "c1": store.StepPassed, "c2": store.StepPassed, "b": store.StepPassed}
			if got := h.rows(t, "order"); !equalMaps(got, want) {
				t.Errorf("order step rows = %v, want %v", got, want)
			}
			if r := h.records("order", "step_end")["b"]; r.Attrs["duration_ms"] == nil || r.JobID != "order" {
				t.Errorf("step_end b = %+v, want a duration and the node", r)
			}
		})

		t.Run("IndependentStepsRunAtOnce", func(t *testing.T) {
			h.wantNode(t, "parallel", sparkwing.Success, "")
		})

		t.Run("FailFastCancelsSiblingsAndRunsFinally", func(t *testing.T) {
			h.wantNode(t, "failfast", sparkwing.Failed, `step "reject": rejected`)
			h.wantLog(t, "failfast", []string{"slow saw its cancel", "cleanup saw env-7"}, []string{"pending ran"})
			want := map[string]string{"setup": "success", "slow": "cancelled", "reject": "failed", "pending": "cancelled", "cleanup": "success"}
			if got := h.outcomes(t, "failfast"); !equalMaps(got, want) {
				t.Errorf("failfast step outcomes = %v, want %v", got, want)
			}
			if got := h.rows(t, "failfast"); got["slow"] != store.StepCancelled || got["pending"] != store.StepCancelled || got["reject"] != store.StepFailed || got["cleanup"] != store.StepPassed {
				t.Errorf("failfast step rows = %v", got)
			}
			ff, ok := h.records("failfast", "work_fail_fast")["reject"]
			if !ok || ff.Attrs["trigger_step"] != "reject" || fmt.Sprint(ff.Attrs["cancelled_steps"]) != "[pending slow]" || ff.Attrs["cancelled_siblings"] != 2 {
				t.Errorf("work_fail_fast = %+v (found %v), want reject cancelling pending and slow", ff, ok)
			}
			failure, firstCancel := -1, -1
			for i, r := range h.logs.records["failfast"] {
				if r.Event != "step_end" {
					continue
				}
				if r.Msg == "reject" {
					failure = i
				}
				if r.Attrs["outcome"] == "cancelled" && firstCancel == -1 {
					firstCancel = i
				}
			}
			if failure == -1 || failure > firstCancel {
				t.Errorf("the failure (%d) is recorded after the first cancellation (%d)", failure, firstCancel)
			}
		})

		t.Run("CollectAllFinishesSiblingsButHoldsDependents", func(t *testing.T) {
			h.wantNode(t, "collect", sparkwing.Failed, "rejected")
			h.wantLog(t, "collect", []string{"independent finished"}, []string{"dependent ran"})
			if _, ok := h.records("collect", "work_fail_fast")["reject"]; ok {
				t.Error("collect-all wrote a fail-fast record")
			}
		})

		t.Run("ContinueOnErrorLetsDependentsRun", func(t *testing.T) {
			h.wantNode(t, "continue", sparkwing.Failed, "rejected")
			h.wantLog(t, "continue", []string{"dependent ran"}, nil)
		})

		t.Run("OptionalMasksTheRollup", func(t *testing.T) {
			h.wantNode(t, "optional", sparkwing.Success, "")
			h.wantLog(t, "optional", []string{"real work ran"}, nil)
			if got := h.outcomes(t, "optional")["best-effort"]; got != "failed" {
				t.Errorf("best-effort outcome = %s, want failed", got)
			}
		})

		t.Run("SkipIfSkipsAStepAndSatisfiesItsDependents", func(t *testing.T) {
			h.wantNode(t, "skips", sparkwing.Success, "")
			h.wantLog(t, "skips", []string{"ran b"}, []string{"ran a"})
			if r := h.records("skips", "step_skipped")["a"]; r.Attrs["outcome"] != "skipped" || r.Attrs["reason"] != nil {
				t.Errorf("step_skipped a = %+v, want outcome skipped and no reason", r)
			}
			if got := h.rows(t, "skips")["a"]; got != store.StepSkipped {
				t.Errorf("a row = %q, want skipped", got)
			}
		})

		t.Run("APanickingSkipIfFailsItsStep", func(t *testing.T) {
			h.wantNode(t, "skippanic", sparkwing.Failed, "guard blew up")
			h.wantLog(t, "skippanic", nil, []string{"guarded ran"})
		})

		t.Run("APanickingStepFailsTheNode", func(t *testing.T) {
			h.wantNode(t, "panics", sparkwing.Failed, "dropped a wrench")
		})

		t.Run("StartAtSkipsUpstreamSteps", func(t *testing.T) {
			h.wantNode(t, "range", sparkwing.Success, "")
			h.wantLog(t, "range", []string{"ran r-b", "ran r-c"}, []string{"ran r-a"})
			if r := h.records("range", "step_skipped")["r-a"]; r.Attrs["reason"] != "upstream of --start-at=r-b" {
				t.Errorf("step_skipped r-a = %+v, want the window's reason", r)
			}
		})
	})

	t.Run("DryRunRunsOnlyStepsThatDeclareIt", func(t *testing.T) {
		h := host(t, "run-dry", "dry", func(c *Config) { c.Run.DryRun = true })
		h.wantNode(t, "dry", sparkwing.Success, "")
		h.wantLog(t, "dry", []string{"dry body", "safe ran"}, []string{"real body", "plain ran"})
		if r := h.records("dry", "step_skipped")["plain"]; r.Attrs["reason"] != "no_dry_run_defined" {
			t.Errorf("step_skipped plain = %+v, want no_dry_run_defined", r)
		}
	})

	t.Run("AnUnknownBoundFailsTheRun", func(t *testing.T) {
		h := host(t, "run-badrange", "main", func(c *Config) { c.StopAt = "r-bb" })
		if h.res.Status != "failed" || !strings.Contains(errText(h.res.Err), `--sw-stop-at "r-bb" references unknown step "r-bb"; did you mean "r-b"?`) {
			t.Errorf("status=%s err=%v, want failed naming the closest step", h.res.Status, h.res.Err)
		}
		if len(h.res.Order) != 0 {
			t.Errorf("nodes %v started under an unknown bound", h.res.Order)
		}
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
			if !equalMaps(got, tc.want) {
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
