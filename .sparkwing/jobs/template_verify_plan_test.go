package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	templates "github.com/sparkwing-dev/sparks-core/templates"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestTemplateVerifyPlanUsesMeasuredResourcesAndBoundsFanout(t *testing.T) {
	host, sourcePlan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{}, sparkwing.RunContext{})
	})
	declaration, err := host.Plan(t.Context(), "TemplateVerify", nil)
	if err != nil {
		t.Fatal(err)
	}
	generator := declaration.Jobs[0].Closures.Generate[0]
	if !slices.Equal(declaration.Jobs[1].NeedsGenerated, []string{generator}) {
		t.Fatal("summary does not wait for every generated verification")
	}
	registry := map[string]templates.Template{
		"new-local-template": {Manifest: templates.Manifest{Name: "new-local-template"}},
		"another-template":   {Manifest: templates.Manifest{Name: "another-template"}},
	}
	raw, err := json.Marshal(verifyEnv{Templates: registry})
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := host.Eval(t.Context(), sourcePlan, "build-cli", generator, raw)
	if err != nil {
		t.Fatal(err)
	}
	plan := decodePipeline(t, expanded)

	summaryHost := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"build-cli": raw}})
	summary, err := summaryHost.RunStep(t.Context(), sourcePlan, "summary", "run")
	if err != nil {
		t.Fatal(err)
	}
	var total TemplateVerifySummary
	if err := json.Unmarshal(summary, &total); err != nil {
		t.Fatal(err)
	}
	if total.Total != len(registry) {
		t.Fatalf("summary total = %d, want %d", total.Total, len(registry))
	}
	emptyHost, emptyPlan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{}, sparkwing.RunContext{})
	})
	if _, err := emptyHost.Eval(t.Context(), emptyPlan, "build-cli", generator, json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty registry produced a successful verification expansion")
	}

	count := 0
	for _, node := range plan.Jobs {
		if !strings.HasPrefix(node.ID, "verify-") {
			continue
		}
		count++
		if len(node.Concurrency) != 1 {
			t.Fatalf("%s has no fanout bound", node.ID)
		}
		limit := node.Concurrency[0]
		if limit.Group != "template-verify" || limit.Scope != sparkwing.ScopeRun || limit.OnLimit != sparkwing.Queue || limit.Capacity != 4 {
			t.Fatalf("%s concurrency = %q %+v", node.ID, limit.Group, limit)
		}
	}
	if count != len(registry) {
		t.Fatalf("bounded templates = %d, want %d", count, len(registry))
	}
}

func TestTemplateVerifyPlanShapeIsIndependentOfProofReuse(t *testing.T) {
	nodeIDs := func(in TemplateVerifyArgs) []string {
		plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
			return (TemplateVerify{}).Plan(context.Background(), plan, in, sparkwing.RunContext{})
		})
		var ids []string
		for _, node := range plan.Jobs {
			ids = append(ids, node.ID)
		}
		sort.Strings(ids)
		return ids
	}
	reuse := nodeIDs(TemplateVerifyArgs{})
	exhaustive := nodeIDs(TemplateVerifyArgs{Exhaustive: true})
	if !slices.Equal(reuse, exhaustive) {
		t.Fatalf("reuse changed the plan shape:\n reuse:      %v\n exhaustive: %v", reuse, exhaustive)
	}
}

func TestRequireTemplateVerifyDisk_FailsBeforeWorkBelowFloor(t *testing.T) {
	err := requireTemplateVerifyDisk(9 << 30)
	if err == nil || !strings.Contains(err.Error(), "10 GiB") {
		t.Fatalf("low-disk preflight = %v, want actionable 10 GiB floor", err)
	}
	if err := requireTemplateVerifyDisk(10 << 30); err != nil {
		t.Fatalf("disk at floor rejected: %v", err)
	}
}

func TestTemplateVerifyOnlyPlansSelectedTemplate(t *testing.T) {
	name := "new-local-template"
	registry := map[string]templates.Template{
		name:               {Manifest: templates.Manifest{Name: name}},
		"another-template": {Manifest: templates.Manifest{Name: "another-template"}},
	}
	raw, err := json.Marshal(verifyEnv{Templates: registry})
	if err != nil {
		t.Fatal(err)
	}
	for _, exhaustive := range []bool{false, true} {
		host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
			return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{Only: name, Exhaustive: exhaustive}, sparkwing.RunContext{})
		})
		declaration, err := host.Plan(t.Context(), "TemplateVerify", nil)
		if err != nil {
			t.Fatal(err)
		}
		generator := declaration.Jobs[0].Closures.Generate[0]
		expanded, err := host.Eval(t.Context(), plan, "build-cli", generator, raw)
		if err != nil {
			t.Fatal(err)
		}
		selected := decodePipeline(t, expanded)
		if len(selected.Jobs) != 1 || selected.Jobs[0].ID != "verify-"+name {
			t.Fatalf("selected jobs = %+v", selected.Jobs)
		}
		summaryHost := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"build-cli": raw}})
		output, err := summaryHost.RunStep(t.Context(), plan, "summary", "run")
		if err != nil {
			t.Fatal(err)
		}
		var summary TemplateVerifySummary
		if err := json.Unmarshal(output, &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Total != 1 || summary.Only != name {
			t.Fatalf("selected summary = %+v", summary)
		}
	}
}

func TestTemplateVerifyOnlyRejectsUnknownName(t *testing.T) {
	host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{Only: "missing-template-name"}, sparkwing.RunContext{})
	})
	raw, err := json.Marshal(verifyEnv{Templates: map[string]templates.Template{"present": {Manifest: templates.Manifest{Name: "present"}}}})
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := host.Plan(t.Context(), "TemplateVerify", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Eval(t.Context(), plan, "build-cli", declaration.Jobs[0].Closures.Generate[0], raw)
	if err == nil {
		t.Fatalf("unknown selection = %v", err)
	}
}
