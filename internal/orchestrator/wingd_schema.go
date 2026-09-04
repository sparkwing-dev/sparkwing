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

// safety: `sparkwing daemon restart` respawns the installed build, so both
// machine-wide remedies replace a daemon other repositories share. The
// isolated home leaves it alone, and only from a sparkwing new enough for the
// store, which is why the command says so.
func storeSchemaRemedy(diagnosis string, selfSchema int) error {
	return fmt.Errorf("%w: %s. "+
		"Install a sparkwing that understands schema %d, or set %s to a binary that does and stop the daemon so the next run brings it up. "+
		"To leave this machine's daemon where it is, give the run a home of its own and start it from a sparkwing that understands schema %d: %s. "+
		"`sparkwing daemon restart` respawns the same build",
		ErrDaemonStoreSchemaTooOld, diagnosis, selfSchema, wingdclient.HostBinEnv, selfSchema, isolatedHomeCommand())
}

func describeVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
