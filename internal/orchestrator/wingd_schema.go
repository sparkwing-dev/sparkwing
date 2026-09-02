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
	return fmt.Errorf("%w: daemon %s understands runs-store schema %d, this binary is %s at schema %d, "+
		"and the store both share is migrated to the newer one. "+
		"Run `sparkwing daemon restart` to replace the daemon with a matching build, "+
		"or set SPARKWING_HOME to run against a daemon of your own",
		ErrDaemonStoreSchemaTooOld, describeVersion(daemonVersion), daemonSchema, describeVersion(selfVersion), selfSchema)
}

func describeVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
