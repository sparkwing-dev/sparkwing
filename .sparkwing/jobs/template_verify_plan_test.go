package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTemplateVerifyPlanUsesMeasuredResourcesAndBoundsFanout(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (TemplateVerify{}).Plan(context.Background(), plan, TemplateVerifyArgs{}, sparkwing.RunContext{})
	})

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
	if count != len(verifyTemplates) {
		t.Fatalf("bounded templates = %d, want %d", count, len(verifyTemplates))
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
	name := verifyTemplates[0].Manifest.Name
	for _, exhaustive := range []bool{false, true} {
		host, plan, doc := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
			return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{Only: name, Exhaustive: exhaustive}, sparkwing.RunContext{})
		})
		var ids []string
		for _, job := range doc.Jobs {
			ids = append(ids, job.ID)
		}
		sort.Strings(ids)
		want := []string{"build-cli", "summary", "verify-" + name}
		if !slices.Equal(ids, want) {
			t.Fatalf("selected plan = %v, want %v", ids, want)
		}
		output, err := host.RunStep(t.Context(), plan, "summary", "run")
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
	plan := sparkwing.NewPlan()
	err := (TemplateVerify{}).Plan(t.Context(), plan, TemplateVerifyArgs{Only: "missing-template-name"}, sparkwing.RunContext{})
	if err == nil || !strings.Contains(err.Error(), "unknown template") {
		t.Fatalf("unknown selection = %v", err)
	}
}

func TestTemplateVerifyFullSummaryCoversRegistry(t *testing.T) {
	host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (TemplateVerify{}).Plan(ctx, plan, TemplateVerifyArgs{}, sparkwing.RunContext{})
	})
	output, err := host.RunStep(t.Context(), plan, "summary", "run")
	if err != nil {
		t.Fatal(err)
	}
	var summary TemplateVerifySummary
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Total != len(verifyTemplates) || summary.Only != "" {
		t.Fatalf("full summary = %+v", summary)
	}
}
