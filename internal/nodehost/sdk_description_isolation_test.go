package nodehost

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func FuzzSDKDescribeIsolatesFailuresAndSecretDeclarations(f *testing.F) {
	f.Add("TOKEN", false)
	f.Add("private\x00\n秘密", true)
	f.Fuzz(func(t *testing.T, value string, reversed bool) {
		secret := "private-" + hex.EncodeToString([]byte(value))
		for _, selected := range []string{"build", "broken"} {
			p, input, fixture, processes := sdkPlanningFixture(t)
			input.Run.Pipeline = selected
			healthy := nodeproto.PipelineDescription{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &secret}}, Secrets: []nodeproto.SecretDeclaration{{Name: "HEALTHY_TOKEN"}}}
			broken := nodeproto.PipelineDescription{Name: "broken", Args: []nodeproto.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.PlanFailed, Message: "describe pipeline broken: missing deployment setting"}}
			entries := []nodeproto.PipelineDescription{healthy, broken}
			if reversed {
				entries[0], entries[1] = entries[1], entries[0]
			}
			var logs []string
			p.Log = func(_ context.Context, log nodeproto.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodeproto.Request) (any, error) {
					if req.Op == nodeproto.OpDescribe {
						if err := log(ctx, nodeproto.FrameLog{Message: "unknown-secret-from-broken-declaration"}); err != nil {
							return nil, err
						}
						return nodeproto.DescribeDoc{Pipelines: entries}, nil
					}
					return answer(ctx, req)
				}
				return process, err
			}
			prepared, err := p.Prepare(t.Context(), input, fixture.input)
			if selected == "build" {
				if err != nil || len(prepared.Description.Secrets) != 1 || prepared.Description.Secrets[0].Name != "HEALTHY_TOKEN" || prepared.Args["token"] != secret || len(prepared.SecretArgs) != 1 {
					t.Fatalf("healthy pipeline lost its complete declarations: %+v: %v", prepared, err)
				}
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.PlanFailed || !strings.Contains(failure.Message, "missing deployment setting") || prepared.Functions.Plan != nil {
					t.Fatalf("broken pipeline did not refuse with its cause: %+v: %v", prepared, err)
				}
			}
			for _, log := range logs {
				if strings.Contains(log, "unknown-secret") {
					t.Fatalf("partial describe released unclassified output: %s", log)
				}
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("describe did not join cleanup")
				}
			}
		}
	})
}
