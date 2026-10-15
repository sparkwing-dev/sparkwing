package protocolgo

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	RecoveryChainName          = "protocol-go-recovery-chain"
	GeneratedRecoveryChainName = "protocol-go-generated-recovery-chain"
)

type RecoveryChainArgs struct {
	Recipient string `flag:"recipient" required:"true"`
	Mode      string `flag:"notification" default:"email" enum:"email,chat"`
}

type RecoveryChainPipeline struct{}

func (RecoveryChainPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	recovery := plan.Approval("approve", sparkwing.ApprovalConfig{}).
		OnFailure("recover", func(context.Context) error { return nil })
	recovery.OnFailure("notify", &argumentJob[RecoveryChainArgs]{})
	return nil
}

type GeneratedRecoveryChainPipeline struct{}

type GeneratedRecoveryInputs struct {
	Extra map[string]string `flag:",extra"`
}

type GeneratedRecoveryArgs struct {
	Mode string `flag:"notification" default:"email" enum:"email,chat"`
}

func (GeneratedRecoveryChainPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ GeneratedRecoveryInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "source", func(context.Context) error { return nil }).Generate(func(_ context.Context, expansion *sparkwing.Plan) error {
		recovery := expansion.Approval("approve", sparkwing.ApprovalConfig{}).
			OnFailure("recover", func(context.Context) error { return nil })
		recovery.OnFailure("notify", &argumentJob[GeneratedRecoveryArgs]{})
		return nil
	})
	return nil
}
