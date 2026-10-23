package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type declarationInputs struct {
	Service string `flag:"service"`
}

type declarationServicesPipeline struct{}

func (declarationServicesPipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in declarationInputs, _ sparkwing.RunContext) error {
	var err error
	switch in.Service {
	case "secret":
		_, err = sparkwing.Secret(ctx, "TOKEN")
	case "variable":
		_, err = sparkwing.Variable(ctx, "PLAIN")
	case "output":
		_ = (sparkwing.Ref[string]{JobID: "producer"}).Get(ctx)
	case "latest":
		_ = sparkwing.RefToLastRun[string]("upstream", "producer").Get(ctx)
	case "child":
		_, err = sparkwing.RunAndAwait[string, sparkwing.NoInputs](ctx, "child", "producer")
	case "oidc":
		_, err = sparkwing.OIDCToken(ctx, "audience")
	case "mask":
		err = sparkwing.Mask(ctx, "declaration-mask")
	case "annotation":
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "declaration annotation"})
	case "log":
		sparkwing.Info(ctx, "declaration log")
	}
	if err != nil {
		return err
	}
	sparkwing.Job(plan, "work", func(context.Context) error { return nil })
	return nil
}

func init() {
	sparkwing.Register[declarationInputs]("acceptance-declaration-services", func() sparkwing.Pipeline[declarationInputs] {
		return declarationServicesPipeline{}
	})
}

func testGoSDKDeclarationServices(t *testing.T) {
	callbacks := 0
	host := sparkwingtest.New(sparkwingtest.Options{
		Secrets: map[string]sparkwingtest.Secret{
			"TOKEN": {Value: "fixture-secret", Masked: true},
			"PLAIN": {Value: "fixture-plain"},
		},
		Outputs: map[string]json.RawMessage{"producer": json.RawMessage(`"fixture-output"`)},
		Latest: func(context.Context, string, string) (json.RawMessage, error) {
			callbacks++
			return json.RawMessage(`"fixture-latest"`), nil
		},
		RunPipeline: func(context.Context, string, map[string]string) (json.RawMessage, error) {
			callbacks++
			return json.RawMessage(`"fixture-child"`), nil
		},
		OIDCToken: func(context.Context, string) (string, error) {
			callbacks++
			return "fixture-token", nil
		},
	})
	t.Cleanup(func() { _ = host.Close() })
	for _, service := range []string{"secret", "variable", "output", "latest", "child", "oidc", "mask", "annotation"} {
		t.Run(service, func(t *testing.T) {
			recordLeaf(t)
			_, err := host.Plan(t.Context(), "acceptance-declaration-services", map[string]string{"service": service})
			code := apierr.PlanFailed
			if service == "output" || service == "latest" || service == "annotation" {
				code = apierr.SDKError
			}
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != code {
				t.Fatalf("plan-time %s access = %v, want %s", service, err, code)
			}
		})
	}
	if callbacks != 0 {
		t.Errorf("planning invoked %d execution service callbacks", callbacks)
	}
	for _, mask := range host.Masks() {
		if mask == "declaration-mask" || mask == "fixture-token" {
			t.Errorf("planning registered execution mask %q", mask)
		}
	}
	if _, err := host.Plan(t.Context(), "acceptance-declaration-services", map[string]string{"service": "log"}); err != nil {
		t.Fatalf("declaration logging: %v", err)
	}
	records := host.Records()
	if len(records) != 1 || records[0].Msg != "declaration log" || records[0].JobID != "" {
		t.Fatalf("declaration records = %+v", records)
	}
}
