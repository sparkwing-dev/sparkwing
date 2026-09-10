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

// safety: `sparkwing update` replaces the binary the daemon respawns from, and
// only the restart swaps the daemon every repository on this machine shares,
// so naming one without the other leaves the operator on the old daemon.
func daemonUpgradeRemedy(selfSchema int) string {
	bin, fromEnv, ok := wingdclient.ResolveHostBin()
	if !ok {
		return fmt.Sprintf("No sparkwing on this machine hosts the daemon; install one that understands schema %d with `%s`",
			selfSchema, installAdvice)
	}
	source := "the `sparkwing` on PATH"
	if fromEnv {
		source = "$" + wingdclient.HostBinEnv
	}
	return fmt.Sprintf("The daemon runs from %s (%s); upgrade it with `sparkwing update`, then `sparkwing daemon restart`, "+
		"or point %s at a binary that understands schema %d and restart the daemon",
		bin, source, wingdclient.HostBinEnv, selfSchema)
}

func describeVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
