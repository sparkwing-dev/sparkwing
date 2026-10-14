package lease_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestKeeperCauses(t *testing.T) {
	acceptance.CheckKeeperCauses(t)
}

func TestKeeperConflictAndStop(t *testing.T) {
	acceptance.CheckKeeperConflictAndStop(t)
}
