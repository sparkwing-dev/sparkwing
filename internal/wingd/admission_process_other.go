//go:build !linux && !darwin && !windows

package wingd

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func admissionProcessIdentity(int) (localhostwire.LocalPeerIdentity, error) {
	return localhostwire.LocalPeerIdentity{}, fmt.Errorf("process ownership inspection unsupported")
}
