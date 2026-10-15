package sparkwingtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type fakeOutput struct {
	Value string
}

type fakePipeline struct{}

func (fakePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "planned", func(context.Context) error { return nil })
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("sparkwingtest-public-fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return fakePipeline{}
	})
}

type fakeProducer struct {
	sparkwing.Base
	sparkwing.Produces[fakeOutput]
}

func (fakeProducer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (fakeOutput, error) {
		return fakeOutput{Value: "ready"}, nil
	}), nil
}

type fakeConsumer struct {
	sparkwing.Base
	Source sparkwing.Ref[fakeOutput]
}

func (j fakeConsumer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		if got := j.Source.Get(ctx).Value; got != "ready" {
			return fmt.Errorf("typed reference = %q", got)
		}
		if runner := sparkwing.Runner(ctx); runner == nil || !runner.HasLabel("os=linux") {
			return errors.New("runner identity is absent")
		}
		secret, err := sparkwing.Secret(ctx, "TOKEN")
		if err != nil || secret != "fixture-secret" {
			return fmt.Errorf("secret = %q, %v", secret, err)
		}
		plain, err := sparkwing.Variable(ctx, "REGION")
		if err != nil || plain != "west" {
			return fmt.Errorf("variable = %q, %v", plain, err)
		}
		if _, err := sparkwing.Variable(ctx, "TOKEN"); err == nil {
			return errors.New("masked secret resolved as a plain variable")
		}
		if _, err := sparkwing.Secret(ctx, "REGION"); err == nil {
			return errors.New("plain variable resolved as a secret")
		}
		token, err := sparkwing.OIDCToken(ctx, "audience")
		if err != nil || token != "fixture-oidc" {
			return fmt.Errorf("OIDC token = %q, %v", token, err)
		}
		if err := sparkwing.Mask(ctx, "dynamic-value"); err != nil {
			return err
		}
		sparkwing.Info(ctx, "secret=%s oidc=%s dynamic=%s", secret, token, "dynamic-value")
		sparkwing.Annotate(ctx, sparkwing.Annotation{
			Level: "warn", Message: "dynamic-value", File: "main.go", Line: 4, Column: 2,
		})
		sparkwing.Summary(ctx, "fixture-secret")
		return nil
	}), nil
}

func TestFakeHostRunsPublicSDKAndMasksRecords(t *testing.T) {
	plan := sparkwing.NewPlan()
	producer := sparkwing.Job(plan, "producer", fakeProducer{})
	sparkwing.Job(plan, "consumer", fakeConsumer{Source: sparkwing.RefTo[fakeOutput](producer)})
	sparkwing.Job(plan, "failure", func(context.Context) error { return errors.New("body failed") })

	audience := ""
	host := sparkwingtest.New(sparkwingtest.Options{
		Runner: sparkwing.RunnerInfo{Name: "fixture", Type: "local", Labels: []string{"os=linux"}},
		Secrets: map[string]sparkwingtest.Secret{
			"TOKEN":  {Value: "fixture-secret", Masked: true},
			"REGION": {Value: "west"},
		},
		Outputs: map[string]json.RawMessage{"producer": json.RawMessage(`{"Value":"ready"}`)},
		OIDCToken: func(_ context.Context, got string) (string, error) {
			audience = got
			return "fixture-oidc", nil
		},
	})
	t.Cleanup(func() { _ = host.Close() })

	output, err := host.RunStep(t.Context(), plan, "producer", "run")
	if err != nil {
		t.Fatal(err)
	}
	var produced fakeOutput
	if err := json.Unmarshal(output, &produced); err != nil || produced.Value != "ready" {
		t.Fatalf("typed output = %s, %v", output, err)
	}
	if _, err := host.RunStep(t.Context(), plan, "consumer", "run"); err != nil {
		t.Fatal(err)
	}
	if audience != "audience" {
		t.Fatalf("OIDC audience = %q", audience)
	}
	for _, want := range []string{"fixture-secret", "fixture-oidc", "dynamic-value"} {
		found := false
		for _, value := range host.Masks() {
			found = found || value == want
		}
		if !found {
			t.Errorf("mask %q was not acknowledged", want)
		}
	}
	var log, annotation, summary bool
	for _, record := range host.Records() {
		if strings.Contains(record.Msg, "fixture-secret") || strings.Contains(record.Msg, "fixture-oidc") || strings.Contains(record.Msg, "dynamic-value") {
			t.Errorf("record leaked masked value: %+v", record)
		}
		switch record.Event {
		case "annotation":
			annotation = record.Level == "warn" && record.Attrs["file"] == "main.go" && record.Attrs["line"] == json.Number("4")
		case "summary":
			summary = record.Msg == "***"
		default:
			log = log || strings.Contains(record.Msg, "secret=***")
		}
	}
	if !log || !annotation || !summary {
		t.Fatalf("missing public records: log=%t annotation=%t summary=%t records=%+v", log, annotation, summary, host.Records())
	}
	if _, err := host.RunStep(t.Context(), plan, "failure", "run"); err == nil || !strings.Contains(err.Error(), "body failed") {
		t.Fatalf("step failure = %v", err)
	}
	if _, err := host.RunStep(t.Context(), plan, "absent", "run"); err == nil {
		t.Fatal("unknown job succeeded")
	}
}

func TestFakeHostPlansRegisteredPipeline(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	plan, err := host.Plan(t.Context(), "sparkwingtest-public-fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "planned" {
		t.Fatalf("plan jobs = %+v", plan.Jobs)
	}
	if _, err := host.Plan(t.Context(), "unregistered-fixture", nil); err == nil {
		t.Fatal("unknown pipeline planned successfully")
	}
}

func TestFakeHostEvaluatesGeneratorAndClosesContexts(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", fakeProducer{})
	reference := sparkwing.RefTo[fakeOutput](source)
	generated := source.Generate(func(ctx context.Context, expanded *sparkwing.Plan) error {
		value := reference.Get(ctx)
		sparkwing.Job(expanded, "child-"+value.Value, func(context.Context) error { return nil })
		return nil
	})
	host := sparkwingtest.New(sparkwingtest.Options{})
	defer host.Close()
	output, err := host.Eval(t.Context(), plan, "source", generated.ID(), json.RawMessage(`{"Value":"ready"}`))
	if err != nil {
		t.Fatal(err)
	}
	var jobs []struct {
		ID     string   `json:"id"`
		Needs  []string `json:"deps"`
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(output, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "child-ready" || len(jobs[0].Needs) != 1 || jobs[0].Needs[0] != "source" ||
		len(jobs[0].Groups) != 1 || jobs[0].Groups[0] != generated.ID() {
		t.Fatalf("generated jobs = %+v", jobs)
	}
	if _, err := host.Eval(t.Context(), plan, "source", "absent-closure", nil); err == nil {
		t.Fatal("unknown closure evaluated successfully")
	}

	ctx := host.Context(t.Context())
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("closing fake host did not cancel its contexts")
	}
}

func TestFakeHostReportsGeneratorFailure(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", fakeProducer{})
	generated := source.Generate(func(context.Context, *sparkwing.Plan) error {
		return errors.New("expansion failed")
	})
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	if _, err := host.Eval(t.Context(), plan, "source", generated.ID(), nil); err == nil ||
		!strings.Contains(err.Error(), "expansion failed") {
		t.Fatalf("generator failure = %v", err)
	}
}

func TestFakeHostCleanupDoesNotUseAmbientLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "ambient-run")
	t.Setenv("SPARKWING_JOB_ID", "ambient-job")
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	release, err := cleanup.Register(host.Context(t.Context()), cleanup.Spec{Argv: []string{"fixture-cleanup"}})
	defer release()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Unavailable {
		t.Errorf("cleanup error = %v, want unavailable", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fake cleanup wrote ambient files: %v", entries)
	}
}

func TestFakeHostSummaryUsesMessageForMarkdown(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{Secrets: map[string]sparkwingtest.Secret{
		"TOKEN": {Value: "fixture-secret", Masked: true},
	}})
	t.Cleanup(func() { _ = host.Close() })
	attrs, err := json.Marshal(map[string]string{"markdown": strings.Repeat("x", int(limits.AnnotationBytes)+1)})
	if err != nil {
		t.Fatal(err)
	}
	sdkserve.ServicesFrom(host.Context(t.Context())).Emit(nodeproto.FrameLog{
		Event: "summary", Message: "fixture-secret", Attrs: attrs,
	})
	records := host.Records()
	if len(records) != 1 || records[0].Msg != "***" || records[0].Attrs["markdown"] != records[0].Msg {
		t.Fatal("summary markdown did not use the masked message")
	}
}
