//go:build !windows && !darwin

package procgroup

import "context"

func processTable(ctx context.Context, withSessions bool) ([]Info, error) {
	return psProcessTable(ctx, withSessions)
}
