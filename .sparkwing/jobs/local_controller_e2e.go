package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// LocalControllerE2EArgs selects the independently built controller under test.
type LocalControllerE2EArgs struct {
	Binary string `flag:"binary" desc:"Absolute path to a built Sparkwing CLI with dashboard assets"`
}

// LocalControllerE2E exercises local controller features in private homes.
type LocalControllerE2E struct{ sparkwing.Base }

func (LocalControllerE2E) Plan(_ context.Context, plan *sparkwing.Plan, in LocalControllerE2EArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "scenarios", func(ctx context.Context) error {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("local controller feature scenarios require Linux")
		}
		if !filepath.IsAbs(in.Binary) {
			return fmt.Errorf("--binary must name an absolute built CLI path")
		}
		_, err := sparkwing.Exec(ctx, "go", "test", "-p", "2", "-count=1", "-timeout=20m", "-v", "-run", "^TestLocalControllerFeatureE2E$", "./tests/acceptance").
			Dir(sparkwing.WorkDir()).Env("GOWORK", "off").Env("GOMAXPROCS", "2").Env("SPARKWING_LOCAL_E2E_BINARY", in.Binary).Run()
		return err
	}).Cores(2)
	return nil
}

func init() {
	sparkwing.Register("LocalControllerE2E", func() sparkwing.Pipeline[LocalControllerE2EArgs] { return LocalControllerE2E{} })
}
