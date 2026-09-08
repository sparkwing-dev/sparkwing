package sessionledger

import (
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func ownBirth() (string, error) { return procgroup.ProcessBirth(os.Getpid()) }
