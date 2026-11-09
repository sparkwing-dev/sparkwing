package sparkwing

import (
	"context"
	"fmt"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func executeCommand(ctx context.Context, name string, args []string, dir string, env map[string]string) (ExecResult, error) {
	run, ok := sdkserve.HostExec(ctx)
	if !ok {
		return ExecResult{Command: renderCommand(name, args)}, fmt.Errorf("host command service unavailable")
	}
	return hostExecCmd(ctx, run, renderCommand(name, args), name, args, dir, env)
}
