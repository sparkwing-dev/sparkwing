package orchestrator

import (
	"context"
	"errors"
	"fmt"

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
	if skew := daemonStoreSchemaSkew(cl.DaemonVersion(), la.Version, cl.DaemonStoreSchema(), store.ExpectedSchemaVersion()); skew != nil {
		cl.Close()
		return nil, skew
	}
	return cl, nil
}

func daemonStoreSchemaSkew(daemonVersion, selfVersion string, daemonSchema, selfSchema int) error {
	if daemonSchema == 0 || daemonSchema >= selfSchema {
		return nil
	}
	// safety: `sparkwing daemon restart` respawns the installed build and a
	// fresh SPARKWING_HOME still spawns the sparkwing on PATH, so neither of
	// the usual remedies moves a source-built client off a released daemon.
	return fmt.Errorf("%w: daemon %s understands runs-store schema %d, this binary is %s at schema %d, "+
		"and the store both share is migrated to the newer one. "+
		"Install a sparkwing that understands schema %d, or set %s to a binary that does and stop the daemon so the next run brings it up. "+
		"`sparkwing daemon restart` respawns the same build, and a fresh SPARKWING_HOME still hosts the daemon from the sparkwing on PATH",
		ErrDaemonStoreSchemaTooOld, describeVersion(daemonVersion), daemonSchema, describeVersion(selfVersion), selfSchema,
		selfSchema, wingdclient.HostBinEnv)
}

func describeVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
