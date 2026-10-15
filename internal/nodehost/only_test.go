package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func hostWith(t *testing.T, ctx context.Context, st *store.Store, cfg Config, doc nodeproto.PlanDoc) RunResult {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	skips, err := onlySkips(doc, cfg.Only, cfg.StartAt, cfg.StopAt)
	if err != nil {
		t.Fatalf("only: %v", err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), onlySkips: skips, validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	runPlanned(ctx, &cfg, run, &res)()
	return res
}

func TestOnlyRunsTheMatchedNodesAndWhatTheyNeed(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fx := &fakeExec{}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "setup", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build-linux", Needs: deps("setup"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build-mac", Needs: deps("setup"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "deploy", Needs: deps("build-linux", "build-mac"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "notify", OnFailureOf: "build-linux", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}

	res := hostWith(t, ctx, st, Config{Execute: fx.run, Only: "build-l*"}, doc)
	if res.Status != "success" {
		t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
	}
	for _, id := range []string{"setup", "build-linux"} {
		wantRow(t, st, id, sparkwing.Success, "")
	}
	for _, id := range []string{"build-mac", "deploy"} {
		wantRow(t, st, id, sparkwing.Skipped, `outside --only="build-l*"`)
		if fx.ran(id) {
			t.Errorf("%s ran outside --sw-only", id)
		}
	}
	wantRow(t, st, "notify", sparkwing.Skipped, "did not fail")
}

func TestOnlyRefusesWhatTheDispatcherRefused(t *testing.T) {
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "test", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	for _, tc := range []struct{ only, start, want string }{
		{"deploy", "", `--only="deploy" matched no jobs (declared: [build test])`},
		{"[", "", `--only="["`},
		{"build", "compile", "--only is mutually exclusive with --start-at / --stop-at"},
	} {
		if _, err := onlySkips(doc, tc.only, tc.start, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("only=%q start=%q: err=%v, want %q", tc.only, tc.start, err, tc.want)
		}
	}
}

func TestDryRunEnvHandsTheSwitchToTheNode(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		input, state, _, _ := executorFixture(t)
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		spec.Resolved.Settings.Options.DryRun.Set, spec.Resolved.Settings.Options.DryRun.Value = true, dryRun
		var err error
		input.Accepted.Spec, err = json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		policy := SDKEnvironment{Store: state}
		planned, err := policy.planning(t.Context(), PlanningProcessInput{Run: nodeproto.RunContext{RunID: "run", Pipeline: "ci", DryRun: dryRun}})
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := policy.job(t.Context(), procenv.Attempt, input.Accepted, input.NodeID, input.Generation, input.SpecHash, input.GrantedRunner)
		if err != nil {
			t.Fatal(err)
		}
		want := "SPARKWING_DRY_RUN=" + strconv.FormatBool(dryRun)
		if !slices.Contains(planned, want) || !slices.Contains(attempt, want) {
			t.Fatalf("plan=%v attempt=%v, want %s", planned, attempt, want)
		}
	}
}
