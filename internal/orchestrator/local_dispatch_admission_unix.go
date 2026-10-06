//go:build !windows

package orchestrator

import "context"

func prepareLocalDispatchAdmission(context.Context, []string) (func(), error) {
	return func() {}, nil
}
