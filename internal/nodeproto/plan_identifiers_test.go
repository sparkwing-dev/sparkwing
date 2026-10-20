package nodeproto

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestVisitPlanIdentifiersCoversValidatorFields(t *testing.T) {
	closure := ClosureSpec{SkipIf: []string{"skip"}, BeforeRun: []string{"before"}, AfterRun: []string{"after"}, Generate: []string{"generate"}, Verify: "verify", MemoKey: "memo"}
	job := JobSpec{Children: []ChildPipeline{{Pipeline: "child", Repository: "git:host/org/repo"}}, ID: "job", Needs: []string{"dep"}, NeedsIfPresent: []string{"maybe"}, NeedsGenerated: []string{"generated"}, Groups: []string{"group"}, Requires: []string{"require"}, Prefers: []string{"prefer"}, Artifacts: []string{"artifact"}, OnFailureOf: "failure", ResultStep: "result", Caches: []CacheSpec{{Name: "cache", Path: "excluded", Resolver: "excluded", KeyScope: "excluded", KeyFiles: []string{"excluded"}}}, Consumes: []ArtifactInput{{Producer: "producer", Into: "excluded"}}, References: []PipelineReference{{Pipeline: "pipeline", Job: "reference"}}, Env: map[string]EnvValue{"ENV": {Value: "excluded"}}, Concurrency: []ConcurrencySpec{{Group: "job-concurrency"}}, Closures: closure, Steps: []StepSpec{{ID: "step", Needs: []string{"step-dep"}, Groups: []string{"step-group"}, Risks: []string{"excluded"}, Closures: closure}}}
	plan := PlanDoc{Jobs: []JobSpec{job}, Concurrency: []ConcurrencySpec{{Group: "plan-concurrency"}}}
	want := map[string]string{"jobs[0].children[0].pipeline": "child", "plan.concurrency[0].group": "plan-concurrency", "jobs[0].id": "job", "jobs[0].deps[0]": "dep", "jobs[0].deps_if_present[0]": "maybe", "jobs[0].needs_generated[0]": "generated", "jobs[0].groups[0]": "group", "jobs[0].requires[0]": "require", "jobs[0].prefers[0]": "prefer", "jobs[0].artifacts[0]": "artifact", "jobs[0].on_failure_of": "failure", "jobs[0].result_step": "result", "jobs[0].caches[0].name": "cache", "jobs[0].consumes[0].producer": "producer", "jobs[0].pipeline_refs[0].pipeline": "pipeline", "jobs[0].pipeline_refs[0].job": "reference", "jobs[0].env": "ENV", "jobs[0].concurrency[0].group": "job-concurrency", "jobs[0].steps[0].id": "step", "jobs[0].steps[0].needs[0]": "step-dep", "jobs[0].steps[0].groups[0]": "step-group"}
	for _, prefix := range []string{"jobs[0].closures.", "jobs[0].steps[0].closures."} {
		for field, value := range map[string]string{"skip_if": "skip", "before_run": "before", "after_run": "after", "generate": "generate", "verify": "verify", "memo_key": "memo"} {
			want[prefix+field+"[0]"] = value
		}
	}
	got := map[string]string{}
	err := VisitPlanIdentifiers(plan, func(value, path string) error {
		if _, ok := got[path]; ok {
			t.Fatalf("duplicate path %s", path)
		}
		got[path] = value
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers = %#v, error %v; want %#v", got, err, want)
	}
}

func FuzzVisitPlanIdentifiers(f *testing.F) {
	for field := uint8(0); field < 35; field++ {
		f.Add("identifier", field)
	}
	f.Add("", uint8(24))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		plan := PlanDoc{Jobs: []JobSpec{secretIdentifierJob(value, field%35%26)}}
		if field%35 >= 26 {
			plan.Jobs = []JobSpec{{ID: "job", Steps: []StepSpec{{ID: "run"}}}}
		}
		switch field % 35 {
		case 26:
			plan.Concurrency = []ConcurrencySpec{{Group: value}}
		case 27:
			plan.Jobs[0].Steps[0].Closures.SkipIf = []string{value}
		case 28:
			plan.Jobs[0].Steps[0].Closures.BeforeRun = []string{value}
		case 29:
			plan.Jobs[0].Steps[0].Closures.AfterRun = []string{value}
		case 30:
			plan.Jobs[0].Steps[0].Closures.Verify = value
		case 31:
			plan.Jobs[0].Steps[0].Closures.MemoKey = value
		case 32:
			plan.Jobs[0].Steps[0].Closures.Generate = []string{value}
		case 33:
			plan.Jobs[0].Caches = []CacheSpec{{Name: value}}
		case 34:
			plan.Jobs[0].References = []PipelineReference{{Pipeline: value, Job: value}}
		}
		found := false
		sentinel := errors.New("visitor stopped")
		calls := 0
		err := VisitPlanIdentifiers(plan, func(candidate, path string) error {
			calls++
			if candidate == value {
				found = true
				return sentinel
			}
			return nil
		})
		optional := field%35 == 9 || field%35 == 10 || field%35 == 19 || field%35 == 20 || field%35 == 30 || field%35 == 31
		if value != "" || !optional {
			if !found || !errors.Is(err, sentinel) {
				t.Fatalf("field %d omitted value %q: %v", field, value, err)
			}
		}
		if found {
			before := calls
			err = VisitPlanIdentifiers(plan, func(string, string) error { return sentinel })
			if !errors.Is(err, sentinel) || before < 1 {
				t.Fatal(fmt.Sprint("visitor error lost: ", err))
			}
		}
	})
}
