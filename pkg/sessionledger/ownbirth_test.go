package sessionledger

import (
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/procgroup"
)

func ownBirth() (string, error) { return procgroup.ProcessBirth(os.Getpid()) }
