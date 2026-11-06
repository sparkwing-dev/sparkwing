//go:build !linux && !darwin

package wingd

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
)

func startUnmanagedLocalRunner(context.Context, crontimer.Host, string) error {
	return errors.New("wingd: unmanaged runner detachment is unavailable on this operating system")
}
