package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// SDKUpgradeE2EArgs selects the CLI under test.
type SDKUpgradeE2EArgs struct {
	Binary string `flag:"binary" desc:"Absolute path to the Sparkwing CLI under test. Default: build ./cmd/sparkwing from this checkout"`
}

// SDKUpgradeE2E walks scaffold, run, pipeline upgrade and run for Go and TypeScript against the
// published engines and registries. It needs network access and stays off the land path.
type SDKUpgradeE2E struct{ sparkwing.Base }

func (SDKUpgradeE2E) ShortHelp() string {
	return "Scaffold, run, upgrade and run a Go and a TypeScript pipeline with a real CLI"
}

func (SDKUpgradeE2E) Plan(_ context.Context, plan *sparkwing.Plan, in SDKUpgradeE2EArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "upgrade-flows", func(ctx context.Context) (result error) {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("the SDK upgrade scenarios require Linux")
		}
		binary := in.Binary
		if binary == "" {
			dir, err := os.MkdirTemp("", "sparkwing-upgrade-e2e-")
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, os.RemoveAll(dir)) }()
			binary = filepath.Join(dir, "sparkwing")
			if _, err := sparkwing.Exec(ctx, "go", "build", "-trimpath", "-o", binary, "./cmd/sparkwing").Dir(sparkwing.WorkDir()).Env("GOWORK", "auto").Run(); err != nil {
				return fmt.Errorf("build the CLI under test: %w", err)
			}
		}
		if !filepath.IsAbs(binary) {
			return fmt.Errorf("--binary must name an absolute CLI path")
		}
		_, err := sparkwing.Exec(ctx, "go", "test", "-count=1", "-timeout=40m", "-v", "-run", "^TestSDKUpgradeE2E$", "./tests/acceptance").
			Dir(sparkwing.WorkDir()).Env("GOWORK", "auto").Env("SPARKWING_UPGRADE_E2E_BINARY", binary).Run()
		return err
	}).Cores(2).Timeout(45 * time.Minute)
	return nil
}

func init() {
	sparkwing.Register("SDKUpgradeE2E", func() sparkwing.Pipeline[SDKUpgradeE2EArgs] { return SDKUpgradeE2E{} })
}
