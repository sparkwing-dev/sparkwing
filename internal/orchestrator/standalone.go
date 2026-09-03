package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const (
	standaloneNoDaemon    = "no-daemon"
	standaloneDaemonOlder = "daemon-older"
	standaloneFloor       = "floor"
)

// hack: an indirection so a test can read the block a run prints.
var standaloneWarningOut io.Writer = os.Stderr

const standaloneLoss = "It cannot see other runs on this machine and they cannot see it, " +
	"so together they may oversubscribe it. Everything else works."

// safety: the sentinels are the whole rule. A failure that names none of them
// is a machine fault rather than a version gap, and running standalone on one
// would hide it behind a run that looks like it worked.
func standaloneReasonFor(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, wingdclient.ErrProtocolTooOld):
		return standaloneFloor
	case errors.Is(err, wingdclient.ErrDaemonTooOld),
		errors.Is(err, wingdclient.ErrDaemonLacksOperation),
		errors.Is(err, client.ErrControllerLacksRoute),
		errors.Is(err, errHostedStoreSkew):
		return standaloneDaemonOlder
	case errors.Is(err, wingdclient.ErrNoDaemonHost),
		errors.Is(err, wingdclient.ErrDaemonHostUnusable),
		errors.Is(err, wingdclient.ErrDaemonHostFailed):
		return standaloneNoDaemon
	}
	return ""
}

func standaloneWarning(reason, daemonVersion, sdkVersion string) string {
	switch reason {
	case standaloneNoDaemon:
		return "sparkwing: no admission daemon is running and no sparkwing is installed to host one, " +
			"so this run is standalone. " + standaloneLoss +
			"\n\n  to host one\n    " + installAdvice + "\n"
	case standaloneDaemonOlder:
		return fmt.Sprintf("sparkwing: the admission daemon (%s) predates this pipeline's SDK (%s), "+
			"so this run is standalone. "+standaloneLoss+
			"\n\n  to update the daemon\n    sparkwing update\n",
			orUnknownVersion(daemonVersion), orUnknownVersion(sdkVersion))
	case standaloneFloor:
		return fmt.Sprintf("sparkwing: this pipeline's SDK (%s) predates what the admission daemon can serve, "+
			"so this run is standalone. "+standaloneLoss+
			"\n\n  to update every repo on this machine\n    sparkwing repos update --apply"+
			"\n\n  to update just this repo\n    sparkwing repos update --apply --repo .\n",
			orUnknownVersion(sdkVersion))
	}
	return ""
}

func orUnknownVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
