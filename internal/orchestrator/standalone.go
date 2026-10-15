package orchestrator

import (
	"errors"
	"os"
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const (
	standaloneNoDaemon    = "no-daemon"
	standaloneDaemonOlder = "daemon-older"
	standaloneDaemonFault = "daemon-fault"
	standaloneFloor       = "floor"
	standaloneForced      = "forced"
	StandaloneStateDBEnv  = "SPARKWING_STATE_DB"
	StandaloneReasonEnv   = "SPARKWING_STANDALONE_REASON"
)

func standaloneReasonFor(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, wingdclient.ErrProtocolTooOld):
		return standaloneFloor
	case errors.Is(err, wingdclient.ErrDaemonTooOld),
		errors.Is(err, wingdclient.ErrDaemonLacksOperation),
		errors.Is(err, client.ErrControllerLacksRoute):
		return standaloneDaemonOlder
	case errors.Is(err, wingdclient.ErrNoDaemonHost),
		errors.Is(err, wingdclient.ErrDaemonHostUnusable),
		errors.Is(err, wingdclient.ErrDaemonHostFailed):
		return standaloneNoDaemon
	}
	return ""
}

func parentStateDB() string { return strings.TrimSpace(os.Getenv(StandaloneStateDBEnv)) }

func parentStandaloneReason() string { return strings.TrimSpace(os.Getenv(StandaloneReasonEnv)) }
