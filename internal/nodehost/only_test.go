package nodehost

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOnlyRunsTheMatchedNodesAndWhatTheyNeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "setup", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build-linux", Needs: deps("setup"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build-mac", Needs: deps("setup"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "deploy", Needs: deps("build-linux", "build-mac"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "notify", OnFailureOf: "build-linux", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}

	f := newStoreHostFixtureOn(t, openStore(t), doc, runspec.Options{Only: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "build-l*"}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}})
	skips, err := onlySkips(doc, "build-l*", "", "")
	if err != nil {
		t.Fatal(err)
	}
	executed := make(chan string, len(doc.Jobs))
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		executed <- input.NodeID
		return f.report(ctx, input, store.NodeSuccess)
	})
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	close(executed)
	var ran []string
	for id := range executed {
		ran = append(ran, id)
	}
	if !slices.Equal(ran, []string{"setup", "build-linux"}) {
		t.Fatalf("executed=%v", ran)
	}
	events, err := f.Tenant.ListEventsAfter(ctx, f.Accepted.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"setup", "build-linux", "build-mac", "deploy", "notify"} {
		row, err := f.Tenant.ReadNode(ctx, store.NodeRef{RunID: f.Accepted.RunID, NodeID: id})
		if err != nil {
			t.Fatal(err)
		}
		want, reason := store.NodeSuccess, ""
		if selected, ok := skips[id]; ok {
			want, reason = store.NodeSkipped, selected
		} else if id == "notify" {
			want, reason = store.NodeSkipped, "parent_did_not_fail"
		}
		if row.Status != store.StatusDone || row.Outcome != want {
			t.Errorf("%s: status=%s outcome=%s, want %s", id, row.Status, row.Outcome, want)
		}
		if want != store.NodeSkipped {
			continue
		}
		ends := 0
		for _, event := range events {
			if event.NodeID != id || event.Kind != "job_skipped" {
				continue
			}
			ends++
			var result store.NodeResult
			if err := json.Unmarshal(event.Payload, &result); err != nil || result.Reason != reason {
				t.Errorf("%s: event=%s error=%v, want reason %q", id, event.Payload, err, reason)
			}
		}
		if ends != 1 {
			t.Errorf("%s: skipped events=%d, want 1", id, ends)
		}
	}
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

func TestOwnerRefusesUnknownStepWindowBeforeDispatch(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(strconv.FormatBool(resumed), func(t *testing.T) {
			state, host, input := newOwnerFixture(t, resumed)
			options := runspec.Options{StartAt: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "missing"}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}}
			state.snapshot.Run.Request.Request.Options = options
			var err error
			input.SealedRequest, err = json.Marshal(state.snapshot.Run.Request.Request)
			if err != nil {
				t.Fatal(err)
			}
			if resumed {
				state.snapshot.Run.Spec.Request = state.snapshot.Run.Request
				state.snapshot.Run.Spec.Resolved.Settings.Options = options
				accepted, err := ownerAccepted(state.snapshot.Run)
				if err != nil {
					t.Fatal(err)
				}
				input.Accepted = &accepted
			}
			completion, err := host.Run(t.Context(), input)
			if err == nil || !strings.Contains(err.Error(), "unknown step") || completion.Outcome != "" {
				t.Fatalf("completion=%+v error=%v", completion, err)
			}
			if state.accepted.RunID != "" || slices.Contains(state.trace, "start") {
				t.Fatal("invalid window accepted a plan or started work")
			}
			if resumed {
				if slices.Contains(state.trace, "acquire") || state.finished.Unit.RunID != "" {
					t.Fatal("invalid resumed window acquired source or finished its accepted topology")
				}
			} else if state.finished.Transition.To != store.RunFailed {
				t.Fatal("invalid initial window left its owner unfinished")
			}
		})
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
