package sparkwing_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type deployInputs struct {
	Env     string        `flag:"env" required:"true" desc:"Target environment"`
	Version string        `flag:"version" desc:"Image tag"`
	NoApply bool          `flag:"no-apply" desc:"Preview without applying"`
	Count   int           `flag:"count" default:"3" desc:"Number of replicas"`
	Timeout time.Duration `flag:"timeout" desc:"Deadline"`
	//lint:ignore U1000 fixture field verifies the parser skips fields without a flag tag
	unexported string
}

type deployForDescribe struct{ sparkwing.Base }

func (deployForDescribe) Plan(_ context.Context, plan *sparkwing.Plan, _ deployInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	return nil
}

func TestToKebabCase(t *testing.T) {
	cases := map[string]string{
		"Env":       "env",
		"DryRun":    "dry-run",
		"FooBarBaz": "foo-bar-baz",
		"IDMap":     "id-map",
		"A":         "a",
		"":          "",
	}
	for in, want := range cases {
		if got := sparkwing.ToKebabCase(in); got != want {
			t.Errorf("ToKebabCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribePipelineShape(t *testing.T) {
	sparkwing.Register[deployInputs]("describe-fixture", func() sparkwing.Pipeline[deployInputs] {
		return deployForDescribe{}
	})

	dp, ok, err := sparkwing.DescribePipelineByName("describe-fixture")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !ok {
		t.Fatal("pipeline should be registered")
	}
	if dp.Name != "describe-fixture" {
		t.Errorf("Name = %q, want %q", dp.Name, "describe-fixture")
	}
	if len(dp.Args) != 5 {
		t.Fatalf("Args count = %d, want 5 (excluding unexported), got: %+v", len(dp.Args), dp.Args)
	}

	byName := map[string]sparkwing.DescribeArg{}
	for _, a := range dp.Args {
		byName[a.Name] = a
	}

	env := byName["env"]
	if env.Type != "string" || !env.Required || env.Desc != "Target environment" {
		t.Errorf("env = %+v", env)
	}
	if env.GoName != "Env" {
		t.Errorf("env.GoName = %q, want Env", env.GoName)
	}
	dry := byName["no-apply"]
	if dry.Type != "bool" || dry.Required {
		t.Errorf("no-apply = %+v", dry)
	}
	count := byName["count"]
	if count.Type != "int" || count.Default != "3" {
		t.Errorf("count = %+v", count)
	}
	to := byName["timeout"]
	if to.Type != "duration" {
		t.Errorf("timeout = %+v", to)
	}

	// Round-trip through JSON so the wire shape is locked down. The
	// wing CLI consumes this exact encoding out of the describe
	// subprocess.
	blob, err := json.Marshal(dp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back sparkwing.DescribePipeline
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Args) != len(dp.Args) {
		t.Errorf("round-trip args count mismatch")
	}
}

type populateInputs struct {
	Env       string            `flag:"env" required:"true" desc:"Target environment"`
	Version   string            `flag:"version" desc:"Image tag"`
	NoApply   bool              `flag:"no-apply"`
	Count     int               `flag:"count" default:"3"`
	Timeout   time.Duration     `flag:"timeout"`
	BagFields map[string]string `flag:",extra"`
}

type populatePipe struct {
	sparkwing.Base
	captured populateInputs
}

func (pp *populatePipe) Plan(_ context.Context, plan *sparkwing.Plan, in populateInputs, rc sparkwing.RunContext) error {
	pp.captured = in
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	return nil
}

func TestRegistration_InvokeParsesTypes(t *testing.T) {
	captured := &populatePipe{}
	sparkwing.Register[populateInputs]("populate-fixture", func() sparkwing.Pipeline[populateInputs] {
		return captured
	})
	reg, ok := sparkwing.Lookup("populate-fixture")
	if !ok {
		t.Fatal("not registered")
	}
	_, err := reg.Invoke(context.Background(), map[string]string{
		"env":      "prod",
		"no-apply": "true",
		"count":    "7",
		"timeout":  "1m30s",
		"version":  "v1.2.3",
		"unknown":  "stashed-in-bag",
	}, sparkwing.RunContext{Pipeline: "populate-fixture"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if captured.captured.Env != "prod" {
		t.Errorf("Env = %q", captured.captured.Env)
	}
	if !captured.captured.NoApply {
		t.Error("NoApply false")
	}
	if captured.captured.Count != 7 {
		t.Errorf("Count = %d", captured.captured.Count)
	}
	if captured.captured.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v", captured.captured.Timeout)
	}
	if captured.captured.Version != "v1.2.3" {
		t.Errorf("Version = %q", captured.captured.Version)
	}
	if captured.captured.BagFields["unknown"] != "stashed-in-bag" {
		t.Errorf("bag = %v", captured.captured.BagFields)
	}
}

func TestRegistration_InvokeBadInt(t *testing.T) {
	sparkwing.Register[populateInputs]("populate-bad-int", func() sparkwing.Pipeline[populateInputs] {
		return &populatePipe{}
	})
	reg, _ := sparkwing.Lookup("populate-bad-int")
	_, err := reg.Invoke(context.Background(), map[string]string{
		"env":   "prod",
		"count": "not-a-number",
	}, sparkwing.RunContext{Pipeline: "populate-bad-int"})
	if err == nil {
		t.Fatal("expected error on bad int")
	}
}
