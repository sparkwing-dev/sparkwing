//go:build !qatrust

package sdkinstall

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func qaModuleEnvironment(context.Context, string, engine.Pin, []string) ([]string, error) {
	return nil, nil
}
