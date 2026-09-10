package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrDaemonStoreSchemaTooOld reports that the admission daemon's binary
// understands an older runs-store schema than this binary does, so the daemon
// cannot read the store the two share.
var ErrDaemonStoreSchemaTooOld = errors.New("local admission: the admission daemon cannot read this runs store")

func (la *LocalAdmission) ensureDaemon(ctx context.Context) (*wingdclient.Client, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, la.clientOptions())
	if err != nil {
		return nil, err
	}
	skew := daemonStoreSchemaSkew(
		cl.DaemonVersion(), la.Version,
		cl.DaemonStoreSchema(), cl.DaemonStoreRequirements(),
		store.ExpectedSchemaVersion())
	if skew != nil {
		cl.Close()
		return nil, skew
	}
	return cl, nil
}

func daemonStoreSchemaSkew(daemonVersion, selfVersion string, daemonSchema int, daemonRequirements []string, selfSchema int) error {
	if daemonSchema == 0 {
		return nil
	}
	if daemonRequirements != nil {
		missing := store.MissingRequirements(daemonRequirements, store.KnownRequirements())
		if len(missing) == 0 {
			return nil
		}
		return storeSchemaRemedy(fmt.Sprintf(
			"daemon %s does not understand runs-store requirement(s) %s, which this binary (%s) stamps into the store they share",
			describeVersion(daemonVersion), strings.Join(missing, ", "), describeVersion(selfVersion)), selfSchema)
	}
	if daemonSchema >= selfSchema {
		return nil
	}
	return storeSchemaRemedy(fmt.Sprintf(
		"daemon %s understands runs-store schema %d, this binary is %s at schema %d, "+
			"and the store both share is migrated to the newer one",
		describeVersion(daemonVersion), daemonSchema, describeVersion(selfVersion), selfSchema), selfSchema)
}

func storeSchemaRemedy(diagnosis string, selfSchema int) error {
	return fmt.Errorf("%w: %s. %s", ErrDaemonStoreSchemaTooOld, diagnosis, daemonUpgradeRemedy(selfSchema))
}

// safety: the binary that would spawn a daemon is not always the one that did,
// so the message points at the daemon's own report rather than asserting a
// path, and pairs the upgrade with the restart that swaps the running daemon.
func daemonUpgradeRemedy(selfSchema int) string {
	upgrade := fmt.Sprintf("upgrade that binary to one that understands schema %d, with `sparkwing update` for a "+
		"published release or this repository's `bin/install.sh` for a build newer than any release, then "+
		"`sparkwing daemon restart`", selfSchema)
	bin, fromEnv, ok := wingdclient.ResolveHostBin()
	if !ok {
		return fmt.Sprintf("`sparkwing daemon status` names the build the daemon runs; %s, or set %s to that binary "+
			"and restart the daemon", upgrade, wingdclient.HostBinEnv)
	}
	source := "the `sparkwing` found on PATH"
	if fromEnv {
		source = "$" + wingdclient.HostBinEnv
	}
	return fmt.Sprintf("`sparkwing daemon status` names the build the daemon runs, and %s resolves to %s; %s, "+
		"or point %s at a binary that understands schema %d and restart the daemon",
		source, bin, upgrade, wingdclient.HostBinEnv, selfSchema)
}

func describeVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
