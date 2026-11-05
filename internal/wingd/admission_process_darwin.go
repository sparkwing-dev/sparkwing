package wingd

import (
	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func admissionProcessIdentity(pid int) (localhostwire.LocalPeerIdentity, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	uid := process.Eproc.Ucred.Uid
	return localhostwire.LocalPeerIdentity{GOOS: "darwin", UID: &uid}, nil
}
