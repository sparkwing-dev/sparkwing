package sparkwingtest

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type registrationArgs struct {
	Tokens []string `flag:"tokens" secret:"true" default:"default-token-value,other-default-value"`
}

type registrationPipeline struct{}

func (registrationPipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in registrationArgs, _ sparkwing.RunContext) error {
	sparkwing.Info(ctx, "planning %s", strings.Join(in.Tokens, ","))
	sparkwing.Job(plan, "build", func(context.Context) error { return nil })
	return nil
}

func TestHostRegistersAcceptedSecretListsBeforePlanningOutput(t *testing.T) {
	const name = "sparkwingtest-registration-list-projection"
	sparkwing.Register[registrationArgs](name, func() sparkwing.Pipeline[registrationArgs] { return registrationPipeline{} })
	h := newHost(Options{}).(*host)
	defer h.Close()
	raw := " alpha-token-value , beta-token-value "
	if _, err := h.Plan(t.Context(), name, map[string]string{"tokens": raw}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{raw, "default-token-value,other-default-value", "default-token-value", "other-default-value", "alpha-token-value", "beta-token-value"} {
		if !slices.Contains(h.Masks(), value) {
			t.Errorf("missing registered value %q", value)
		}
	}
	for _, record := range h.Records() {
		for _, value := range []string{"default-token-value", "other-default-value", "alpha-token-value", "beta-token-value"} {
			if strings.Contains(record.Msg, value) {
				t.Fatalf("planning output exposed %q: %+v", value, record)
			}
		}
	}
}

type generatedSecretArgs struct {
	Token string `flag:"token" secret:"true" default:"generated-private-value"`
}

type generatedSecretJob struct {
	sparkwing.Base
	sparkwing.WithArgs[generatedSecretArgs]
}

func (*generatedSecretJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", func(context.Context) error { return nil }), nil
}

func TestHostGatesGeneratorOutputUntilNewSecretsAreRegistered(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
	generator := source.Generate(func(ctx context.Context, expansion *sparkwing.Plan) error {
		sparkwing.Info(ctx, "before declaration generated-private-value")
		sparkwing.Job(expansion, "child", &generatedSecretJob{})
		sparkwing.Info(ctx, "after declaration generated-private-value")
		return nil
	})
	h := newHost(Options{}).(*host)
	defer h.Close()
	if _, err := h.Eval(t.Context(), plan, "source", generator.ID(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.Masks(), "generated-private-value") {
		t.Fatal("generator secret was not registered")
	}
	for _, record := range h.Records() {
		if strings.Contains(record.Msg, "generated-private-value") {
			t.Fatalf("generator output leaked: %+v", record)
		}
	}
}

func TestOutputGateIsScopedToOneOperation(t *testing.T) {
	h := newHost(Options{}).(*host)
	defer h.Close()
	gate := &outputGate{}
	scoped := context.WithValue(t.Context(), outputGateKey{}, gate)
	h.emit(scoped, nodeproto.FrameLog{Message: "gated"}, "one")
	h.emit(t.Context(), nodeproto.FrameLog{Message: "unrelated"}, "two")
	if len(h.Records()) != 1 || h.Records()[0].Msg != "unrelated" {
		t.Fatalf("unrelated output was held: %+v", h.Records())
	}
	h.releaseGate(gate)
	if len(h.Records()) != 2 || h.Records()[1].Msg != "gated" {
		t.Fatalf("gated output was lost: %+v", h.Records())
	}
}

func TestHostRefusesShortAndMarkerSecretListItems(t *testing.T) {
	const name = "sparkwingtest-registration-list-refusal"
	sparkwing.Register[registrationArgs](name, func() sparkwing.Pipeline[registrationArgs] { return registrationPipeline{} })
	for _, raw := range []string{"abc,valid-secret", "prefix-***-suffix,valid-secret"} {
		t.Run(raw, func(t *testing.T) {
			h := newHost(Options{}).(*host)
			defer h.Close()
			_, err := h.Plan(t.Context(), name, map[string]string{"tokens": raw})
			if err == nil || !strings.Contains(err.Error(), "plain config variable") {
				t.Fatalf("refusal = %v", err)
			}
			if len(h.Records()) != 0 {
				t.Fatalf("refused discovery released output: %+v", h.Records())
			}
		})
	}
}

func TestGeneratedRegistrationUsesPreviouslySuppliedOverride(t *testing.T) {
	h := newHost(Options{}).(*host)
	defer h.Close()
	h.args = map[string]string{"tokens": " first-override , second-override "}
	err := h.registerGenerated([]nodeproto.ArgumentDescription{{Name: "tokens", Type: "[]string", Secret: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{" first-override , second-override ", "first-override", "second-override"} {
		if !slices.Contains(h.Masks(), value) {
			t.Errorf("missing generated override %q", value)
		}
	}
}

func TestHostMasksChildChunksAcrossNewlines(t *testing.T) {
	h := newHost(Options{}).(*host)
	defer h.Close()
	secret := "before\nafter"
	if err := h.Mask(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"prefix before\n", "after suffix\n"} {
		h.emit(t.Context(), nodeproto.FrameLog{Event: "exec_line", Stream: "child-stdout", StreamData: base64.StdEncoding.EncodeToString([]byte(raw))}, "build")
	}
	h.emit(t.Context(), nodeproto.FrameLog{Event: "exec_line", Stream: "child-stdout", StreamEnd: true}, "build")
	var joined string
	for _, record := range h.Records() {
		joined += record.Msg + "\n"
	}
	if strings.Contains(joined, secret) || !strings.Contains(joined, "***") {
		t.Fatalf("child stream not masked: %q", joined)
	}
}

func TestHostRefusesEmptyExplicitMask(t *testing.T) {
	h := newHost(Options{}).(*host)
	defer h.Close()
	if err := h.Mask(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "plain config variable") {
		t.Fatalf("empty mask = %v", err)
	}
}

func TestDirectPlanRegistersJobSecretDefaults(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "child", &generatedSecretJob{})
	h := newHost(Options{}).(*host)
	defer h.Close()
	if _, err := h.RunStep(t.Context(), plan, "child", "run"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.Masks(), "generated-private-value") {
		t.Fatal("direct plan secret default was not registered")
	}
}

type secretOutput struct{ Value string }
type secretProducer struct {
	sparkwing.Base
	sparkwing.Produces[secretOutput]
}

func (secretProducer) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", func(context.Context) (secretOutput, error) { return secretOutput{Value: "output-private-value"}, nil }), nil
}

func TestHostWithholdsSecretStepAndEvalOutputs(t *testing.T) {
	for _, operation := range []string{"step", "eval"} {
		t.Run(operation, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			sparkwing.Job(plan, "producer", secretProducer{}).Memo(func(context.Context) (sparkwing.MemoValue, error) {
				return sparkwing.MemoValue("output-private-value"), nil
			})
			h := newHost(Options{Secrets: map[string]Secret{"TOKEN": {Value: "output-private-value", Masked: true}}}).(*host)
			defer h.Close()
			var output []byte
			var err error
			if operation == "step" {
				output, err = h.RunStep(t.Context(), plan, "producer", "run")
			} else {
				output, err = h.Eval(t.Context(), plan, "producer", sdkserve.DefaultOperations.Document(plan).Jobs[0].Closures.MemoKey, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(output) != 0 {
				t.Fatalf("secret output released: %s", output)
			}
			records := h.Records()
			if len(records) != 1 || records[0].Event != "output_withheld" {
				t.Fatalf("withholding diagnostic = %+v", records)
			}
		})
	}
}

func TestIssuedTokenDoesNotRefuseAcceptedIdentifier(t *testing.T) {
	const token = "issued-private-token"
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, token+"-job", secretProducer{})
	h := newHost(Options{OIDCToken: func(context.Context, string) (string, error) { return token, nil }}).(*host)
	defer h.Close()
	if err := h.remember(plan); err != nil {
		t.Fatal(err)
	}
	got, err := h.OIDC(h.Context(t.Context()), "audience")
	if err != nil || got != token {
		t.Fatalf("issued token = %q, %v", got, err)
	}
	if got := h.masker.Mask(token); got != "***" {
		t.Fatalf("issued token unregistered: %q", got)
	}
}

type incompleteDiscoveryPipeline struct{}
type incompleteDiscoveryKey struct{}

func (incompleteDiscoveryPipeline) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	if ctx.Value(incompleteDiscoveryKey{}) == true {
		sparkwing.Info(ctx, "unknown-private-value")
		return fmt.Errorf("unknown-private-value")
	}
	return nil
}

func TestIncompleteDiscoveryWithholdsSelectedPlanningOutput(t *testing.T) {
	const failed = "sparkwingtest-incomplete-discovery"
	const selected = "sparkwingtest-incomplete-discovery-selected"
	sparkwing.Register[sparkwing.NoInputs](failed, func() sparkwing.Pipeline[sparkwing.NoInputs] { return incompleteDiscoveryPipeline{} })
	sparkwing.Register[registrationArgs](selected, func() sparkwing.Pipeline[registrationArgs] { return registrationPipeline{} })
	h := newHost(Options{}).(*host)
	defer h.Close()
	if _, err := h.Plan(context.WithValue(t.Context(), incompleteDiscoveryKey{}, true), selected, nil); err != nil {
		t.Fatal(err)
	}
	if records := h.Records(); len(records) != 0 {
		t.Fatalf("incomplete discovery released planning output: %+v", records)
	}
}
