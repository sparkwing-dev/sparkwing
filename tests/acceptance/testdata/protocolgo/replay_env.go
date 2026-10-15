package protocolgo

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	ReplayEnvName = "protocol-go-replay-env"
	ReplayEnvKey  = "SW_FIXTURE_REPLAY_BODY"
	ReadEnvArg    = "--fixture-read-env"
)

type ReplayEnvInputs struct {
	Executable string `flag:"executable" required:"true"`
}

type ReplayEnvPipeline struct{}

func (ReplayEnvPipeline) Plan(_ context.Context, plan *sparkwing.Plan, in ReplayEnvInputs, _ sparkwing.RunContext) error {
	check := func(ctx context.Context, want string) error {
		got, err := sparkwing.Exec(ctx, in.Executable, ReadEnvArg, ReplayEnvKey).String()
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("command environment = %q, want %q", got, want)
		}
		return nil
	}
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil }).Env(ReplayEnvKey, "source")
	source.Generate(func(ctx context.Context, expansion *sparkwing.Plan) error {
		if err := check(ctx, "outside"); err != nil {
			return err
		}
		child := sparkwing.Job(expansion, "child", func(context.Context) error { return nil }).Env(ReplayEnvKey, "child")
		child.Generate(func(ctx context.Context, nested *sparkwing.Plan) error {
			if err := check(ctx, "outside"); err != nil {
				return err
			}
			sparkwing.Job(nested, "leaf", func(ctx context.Context) error { return check(ctx, "leaf") }).
				Env(ReplayEnvKey, "leaf").SkipIf(func(ctx context.Context) bool {
				if err := check(ctx, "leaf"); err != nil {
					panic(err)
				}
				return false
			})
			return nil
		})
		return nil
	})
	return nil
}
