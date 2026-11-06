//go:build !windows

package wingd

import "github.com/sparkwing-dev/sparkwing/internal/procgroup"

func admissionExecutionEmpty(pid int, birth string) (bool, error) {
	return procgroup.SessionEmpty(procgroup.SessionIdentity{LeaderPID: pid, SessionID: pid, BirthToken: birth})
}

func killAdmissionExecution(pid int, birth string) error {
	return procgroup.KillSession(procgroup.SessionIdentity{LeaderPID: pid, SessionID: pid, BirthToken: birth})
}
