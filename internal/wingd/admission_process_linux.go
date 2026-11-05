package wingd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func admissionProcessIdentity(pid int) (localhostwire.LocalPeerIdentity, error) {
	info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return localhostwire.LocalPeerIdentity{}, fmt.Errorf("process owner unavailable")
	}
	uid := stat.Uid
	return localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid}, nil
}
