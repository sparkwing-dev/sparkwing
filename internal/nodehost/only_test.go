package nodehost

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
	if got := dryRunEnv(&NodeRun{Run: &Run{Context: nodeproto.RunContext{DryRun: true}, validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}}); !slices.Equal(got, []string{"SPARKWING_DRY_RUN=1"}) {
		t.Errorf("env = %v, want the dry-run switch", got)
	}
	if got := dryRunEnv(&NodeRun{Run: &Run{validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}}); got != nil {
		t.Errorf("env = %v, want nothing for a real run", got)
	}
}
