package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

func marker(name string) string { return filepath.Join(os.Getenv("LOCAL_FEATURE_DIR"), name) }

func hold(ctx context.Context, name string) error {
	_, err := sw.Bash(ctx, `echo $$ > "$M.pid"; while [ ! -f "$M.release" ]; do sleep 0.05; done; echo finished-$NAME`).Env("M", marker(name)).Env("NAME", name).Run()
	return err
}

type (
	result   struct{ Value string }
	childJob struct{ sw.Produces[result] }
)

func (childJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	return sw.Step(w, "hold", func(ctx context.Context) (result, error) {
		return result{Value: "child-finished"}, hold(ctx, "child")
	}), nil
}

type child struct{ sw.Base }

func (child) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "held", childJob{}).Cores(0.25)
	return nil
}

type lifecycle struct{ sw.Base }

func (lifecycle) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "running", func(ctx context.Context) error { return hold(ctx, "running") }).Cores(0.25)
	sw.Job(p, "waiting", func(ctx context.Context) error {
		if err := os.WriteFile(marker("waiting.pid"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
			return err
		}
		got, err := sw.RunAndAwait[result, sw.NoInputs](ctx, "child", "held", sw.WithFreshTimeout(3*time.Minute))
		if err != nil {
			return err
		}
		return os.WriteFile(marker("awaited"), []byte(got.Value), 0o600)
	}).StartsChildren("child").Cores(0.25)
	return nil
}

type hostExec struct{ sw.Base }

func (hostExec) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "mask", func(ctx context.Context) error {
		secret := "local-feature-boundary-secret"
		if err := sw.Mask(ctx, secret); err != nil {
			return err
		}
		// The secret crosses the host's 64 KiB output cut on both streams.
		_, err := sw.Bash(ctx, `for stream in 1 2; do { head -c 65530 /dev/zero | tr '\0' x; printf '%s' "$S"; head -c 140000 /dev/zero | tr '\0' y; printf '\nmask-end\n'; } >&$stream; done`).Env("S", secret).Run()
		return err
	}).Cores(0.25)
	sw.Job(p, "background", func(ctx context.Context) error {
		if _, err := sw.Bash(ctx, `sleep 180 </dev/null >/dev/null 2>&1 & echo $! > "$P"`).Env("P", marker("background.pid")).Run(); err != nil {
			return err
		}
		if err := os.WriteFile(marker("command-ended"), nil, 0o600); err != nil {
			return err
		}
		return hold(ctx, "session")
	}).Cores(0.25)
	return nil
}

type cancellation struct{ sw.Base }

func (cancellation) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "tree", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, `sh -c 'sleep 180 & echo $! > "$P.grandchild"; wait' & echo $! > "$P.child"; wait`).Env("P", marker("cancel")).Run()
		return err
	}).Cores(0.25)
	return nil
}

func main() {
	if os.Getenv("LOCAL_FEATURE_DIR") == "" {
		fmt.Fprintln(os.Stderr, "LOCAL_FEATURE_DIR is required")
		os.Exit(1)
	}
	sw.Register("Lifecycle", func() sw.Pipeline[sw.NoInputs] { return lifecycle{} })
	sw.Register("Child", func() sw.Pipeline[sw.NoInputs] { return child{} })
	sw.Register("HostExec", func() sw.Pipeline[sw.NoInputs] { return hostExec{} })
	sw.Register("Cancellation", func() sw.Pipeline[sw.NoInputs] { return cancellation{} })
	sw.Main()
}
