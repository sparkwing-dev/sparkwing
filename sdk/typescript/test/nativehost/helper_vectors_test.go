package nativehost_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestMain(m *testing.M) {
	testleak.Main(m)
}

func TestSharedNestedGenerationGroups(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/helper-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Job    string   `json:"job"`
		Groups []string `json:"groups"`
	}
	var vectors struct {
		Groups []entry `json:"groups"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
	sparkwing.GroupJobs(plan, "display", source)
	source.Generate(func(_ context.Context, generated *sparkwing.Plan) error {
		sparkwing.Job(generated, "child", func(context.Context) error { return nil }).Generate(func(_ context.Context, nested *sparkwing.Plan) error {
			sparkwing.Job(nested, "grandchild", func(context.Context) error { return nil })
			return nil
		})
		return nil
	})
	for _, job := range []string{"source", "child"} {
		result, err := sdkserve.DefaultOperations.Eval(t.Context(), plan, nodeproto.Request{Job: job, Closure: job + "/generate/0", Input: json.RawMessage(`null`)})
		if err != nil || result.Error != nil {
			t.Fatalf("generate %s: %v, %v", job, err, result.Error)
		}
	}
	var got []entry
	for _, job := range sdkserve.DefaultOperations.Document(plan).Jobs {
		slices.Sort(job.Groups)
		got = append(got, entry{Job: job.ID, Groups: job.Groups})
	}
	if !reflect.DeepEqual(got, vectors.Groups) {
		t.Fatalf("groups = %+v, want %+v", got, vectors.Groups)
	}
}
