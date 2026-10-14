package sdkserve_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient/nodeclienttest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const secretsPipeline = "sdkserve-secrets-fixture"

type releaseSecrets struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK,optional"`
}

type secretsFixture struct{ sparkwing.Base }

func (secretsFixture) Secrets() any { return &releaseSecrets{} }

func (secretsFixture) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		sec := sparkwing.PipelineSecrets[releaseSecrets](ctx)
		if sec == nil || sec.DeployToken != "hunter2" || sec.SlackHook != "" {
			return fmt.Errorf("pipeline secrets = %+v", sec)
		}
		return nil
	})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs](secretsPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return secretsFixture{} })
}

func TestRunNodeFillsPipelineSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]nodeclienttest.Secret
		want   nodeproto.RunResultOld
	}{
		{
			name:   "required held, optional unset",
			values: map[string]nodeclienttest.Secret{"DEPLOY_TOKEN": {Value: "hunter2", Masked: true}},
			want:   nodeproto.RunResultOld{Outcome: nodeproto.OutcomeSuccess},
		},
		{
			name: "required unset",
			want: nodeproto.RunResultOld{Outcome: nodeproto.OutcomeFailed, Error: `node "deploy" did not start: ` +
				`this run cannot read 1 required secret it declares: DEPLOY_TOKEN. ` +
				"If the run can do without a secret, declare it optional: tag its field in Secrets() `sw:\"NAME,optional\"`."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declared := []string{"SLACK_HOOK"}
			if tc.values == nil {
				declared = append(declared, "DEPLOY_TOKEN")
			}
			h := nodeclienttest.Start(t, "deploy", tc.values, declared...)
			s := serve(t, h.Client)
			plan := nodeproto.RequestOld{Op: nodeproto.OpPlanOld, Pipeline: secretsPipeline, Args: map[string]string{}, Run: &nodeproto.RunContextOld{RunID: nodeclienttest.RunID}}
			if reply := s.call(plan); !reply.OK {
				t.Fatalf("plan = %+v", reply.Error)
			}
			if got := s.run(nodeproto.OpRunNodeOld, "deploy", ""); got.Outcome != tc.want.Outcome || got.Error != tc.want.Error {
				t.Errorf("run_node = %+v, want %+v", got, tc.want)
			}
		})
	}
}
