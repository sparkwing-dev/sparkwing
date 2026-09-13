package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// safety: the remedy names this service's own flags, because an operator reading
// a refused upload cannot act on the controller's.
const (
	storeCeilingSubject = "the cache store"
	storeCeilingRemedy  = "Delete artifacts, dependency archives or uploads until the next measurement puts " +
		"the store under the ceiling; raise --max-store-bytes or --max-store-objects on sparkwing-cache to accept more."
)

var storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
	Subject: storeCeilingSubject,
	Remedy:  storeCeilingRemedy,
})

// safety: the caller-writable trees are the ones a pipeline can grow without
// bound; the git mirrors grow with the repositories an operator registered.
func storeDirs() []string { return []string{artifactsDir, cacheDir, uploadsDir} }

func measureStore(ctx context.Context) {
	err := storeCeiling.ReconcileWith(ctx, func(context.Context) (objectguard.Usage, error) {
		usage := objectguard.Usage{ObservedAt: time.Now().UTC()}
		for _, dir := range storeDirs() {
			bytes, files, err := treeUsage(dir)
			if err != nil {
				return objectguard.Usage{}, err
			}
			usage.Bytes += bytes
			usage.Objects += files
		}
		return usage, nil
	})
	if err != nil {
		log.Printf("warning: measure cache store: %v", err)
	}
}

// perf: one walk per reconciliation interval, never per request, because each
// stored object already adds its own bytes to the running count.
func treeUsage(dir string) (bytes, files int64, err error) {
	// #nosec G703 -- the roots come from the service's own configuration
	walkErr := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		files++
		return nil
	})
	// safety: a tree the service has not created yet is empty, not a failed
	// measurement; anything else leaves the running count in place.
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return 0, 0, fmt.Errorf("walk %s: %w", dir, walkErr)
	}
	return bytes, files, nil
}

func storeCeilingLoop(ctx context.Context) {
	interval := storeCeiling.Reconcile()
	if !storeCeiling.Enforced() || interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			measureStore(ctx)
		}
	}
}
