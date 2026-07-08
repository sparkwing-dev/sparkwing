package sparkwingruntime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
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
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

func TestDescribePipelineShape(t *testing.T) {
	sparkwing.Register[deployInputs]("describe-fixture", func() sparkwing.Pipeline[deployInputs] {
		return deployForDescribe{}
	})

	dp, ok, err := sparkwingruntime.DescribePipelineByName("describe-fixture")
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

type envDocPipe struct{ sparkwing.Base }

func (envDocPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

func (envDocPipe) EnvVars() []sparkwing.EnvVarDoc {
	return []sparkwing.EnvVarDoc{
		{Name: "NO_CACHE", Description: "Bypass the build cache for this run"},
		{Name: "SMOKE_TIMEOUT", Description: "Per-target smoke deadline", Default: "30s"},
	}
}

type describeJobArgs struct {
	Replicas int    `desc:"replica count"`
	Image    string `desc:"OCI image ref"`
}

type describeJob struct {
	sparkwing.Base
	sparkwing.WithArgs[describeJobArgs]
}

func (j *describeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(_ context.Context) error { return nil }), nil
}

func (describeJob) Schema() (*sparkwing.Schema, error) {
	s := sparkwing.NewSchema[describeJobArgs]()
	s.Field("Replicas").Required().Range(1, 100)
	s.Field("Image").Default("nginx:latest")
	return s.Build()
}

type withJobArgsPipe struct{ sparkwing.Base }

func (withJobArgsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", &describeJob{})
	return nil
}

func TestDescribePipeline_TransitiveWithArgsAppearInDescribe(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("describe-with-args-fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return withJobArgsPipe{}
	})

	dp, ok, err := sparkwingruntime.DescribePipelineByName("describe-with-args-fixture")
	if err != nil || !ok {
		t.Fatalf("describe: ok=%v err=%v", ok, err)
	}

	byName := map[string]sparkwing.DescribeArg{}
	for _, a := range dp.Args {
		byName[a.Name] = a
	}
	rep, hasRep := byName["replicas"]
	if !hasRep {
		t.Fatalf("--replicas not surfaced; got args=%+v", dp.Args)
	}
	if rep.JobID != "deploy" {
		t.Errorf("--replicas JobID = %q, want deploy", rep.JobID)
	}
	if !rep.Required {
		t.Error("--replicas should be marked Required by Schema()")
	}
	if rep.Type != "int" {
		t.Errorf("--replicas Type = %q, want int", rep.Type)
	}
	if rep.Desc != "replica count" {
		t.Errorf("--replicas Desc = %q, want %q", rep.Desc, "replica count")
	}

	img, hasImg := byName["image"]
	if !hasImg {
		t.Fatalf("--image not surfaced; got args=%+v", dp.Args)
	}
	if img.Default != "nginx:latest" {
		t.Errorf("--image Default = %q, want nginx:latest", img.Default)
	}
}

func TestDescribePipeline_EnvVarDocer(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("describe-envvars-fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return envDocPipe{}
	})

	dp, ok, err := sparkwingruntime.DescribePipelineByName("describe-envvars-fixture")
	if err != nil || !ok {
		t.Fatalf("describe: ok=%v err=%v", ok, err)
	}
	if len(dp.EnvVars) != 2 {
		t.Fatalf("EnvVars count = %d, want 2, got %+v", len(dp.EnvVars), dp.EnvVars)
	}
	if dp.EnvVars[0].Name != "NO_CACHE" || dp.EnvVars[0].Description == "" {
		t.Errorf("first env var = %+v", dp.EnvVars[0])
	}
	if dp.EnvVars[1].Default != "30s" {
		t.Errorf("second env var default = %q, want 30s", dp.EnvVars[1].Default)
	}

	blob, _ := json.Marshal(dp)
	if !strings.Contains(string(blob), "env_vars") {
		t.Errorf("JSON missing env_vars key: %s", blob)
	}
}
