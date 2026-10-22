package acceptance_test

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKPlanErrorMasks(t *testing.T) {
	protocolgo.Register()
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, test := range []struct {
		name string
		args map[string]string
	}{
		{"defaults", map[string]string{"fail": "true"}},
		{"prefix-first", map[string]string{"fail": "true", "first": "input-secret", "second": "input-secret-tail"}},
		{"prefix-second", map[string]string{"fail": "true", "first": "input-secret-tail", "second": "input-secret"}},
		{"distinct", map[string]string{"fail": "true", "first": "first-secret", "second": "second-secret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := sparkwingtest.New(sparkwingtest.Options{})
			t.Cleanup(func() { _ = host.Close() })
			_, err := host.Plan(t.Context(), protocolgo.PlanErrorName, test.args)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanFailed || failure.Message != "declaration refused: *** ***" {
				t.Errorf("fake planning error = %v", err)
			}
			process := startProtocolProbe(t, bin)
			sendProtocolProbe(t, process, probeHello())
			protocolProbeReply(t, process, "hello")
			args, err := prepareProtocolArguments(t, process, protocolgo.PlanErrorName, test.args)
			if err != nil {
				t.Fatal(err)
			}
			sendProtocolProbe(t, process, nodeproto.Request{
				ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.PlanErrorName, Args: args,
				Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.PlanErrorName},
			})
			reply := protocolProbeReply(t, process, "plan")
			requireProtocolCode(t, reply, apierr.PlanFailed)
			if reply.Error.Message != "declaration refused: *** ***" {
				t.Errorf("process planning error = %v", reply.Error)
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
