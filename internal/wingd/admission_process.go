package wingd

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func verifyAdmissionProcess(peer localhostwire.LocalPeerIdentity, pid int, birth string) error {
	identity, err := admissionProcessIdentity(pid)
	if err != nil {
		return fmt.Errorf("inspect admission holder: %w", err)
	}
	if _, err := LocalSubject(identity, peer); err != nil {
		return fmt.Errorf("admission holder belongs to another account: %w", err)
	}
	actual, err := admissionProcessBirth(pid)
	if err != nil {
		return fmt.Errorf("inspect admission holder start time: %w", err)
	}
	if actual != birth {
		return fmt.Errorf("admission holder PID %d was reused", pid)
	}
	return nil
}
