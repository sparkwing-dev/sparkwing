package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

// safety: the remedy names what this binary offers, because an operator reading a
// refused upload cannot act on the controller's verbs and the cache serves no
// delete of its own.
const (
	storeCeilingSubject = "the cache store"
	storeCeilingRemedy  = "Free space on the cache volume, then POST /admin/store-ceiling/measure to " +
		"measure it again; POST /admin/store-ceiling/thaw accepts writes until the next measurement. " +
		"Raise --max-store-bytes or --max-store-objects on sparkwing-cache to accept more."
)

var storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
	Subject: storeCeilingSubject,
	Remedy:  storeCeilingRemedy,
})

// safety: every tree on the cache volume counts, the git mirrors included,
// because a mirror fills the same disk a pipeline's writes need.
func storeDirs() []string {
	dirs := []string{artifactsDir, cacheDir, uploadsDir, repoDir}
	if _, err := os.Stat(teamsDir); err == nil {
		dirs = append(dirs, teamsDir)
	}
	return dirs
}

// safety: the git mirrors and in-flight uploads are never evicted; every other
// file on the volume is a binary, archive or artifact a miss rebuilds, so a full
// volume gives those up rather than refusing the write that would fail a run.
func evictableDirs() []string { return []string{artifactsDir, cacheDir, teamsDir} }

var evictMu sync.Mutex

// safety: a volume at its ceiling evicts its least recently read files, by the
// volume's access times, down to seven eighths of the ceiling before it refuses.
func admitStoreWrite() error {
	if !storeCeiling.Frozen() {
		return nil
	}
	evictMu.Lock()
	if storeCeiling.Frozen() {
		evictLeastRecentlyRead()
	}
	evictMu.Unlock()
	return storeCeiling.Allow()
}

type evictable struct {
	path string
	size int64
	read time.Time
}

func evictLeastRecentlyRead() {
	state := storeCeiling.State()
	var files []evictable
	var bytes, objects int64 = state.Bytes, state.Objects
	for _, dir := range evictableDirs() {
		// #nosec G703 -- the roots come from the service's own configuration
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// safety: a file another eviction or a delete removed mid-walk is skipped.
			if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
				files = append(files, evictable{path: path, size: info.Size(), read: lastRead(info)})
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: list %s for eviction: %v", dir, err)
		}
	}
	slices.SortFunc(files, func(a, b evictable) int { return a.read.Compare(b.read) })
	under := func() bool {
		return (state.MaxBytes <= 0 || bytes <= state.MaxBytes/8*7) &&
			(state.MaxObjects <= 0 || objects <= state.MaxObjects/8*7)
	}
	evicted, freed := 0, int64(0)
	for _, f := range files {
		if under() {
			break
		}
		// #nosec G703 -- a path the walk above found under the service's own roots
		if err := os.Remove(f.path); err != nil {
			continue
		}
		storeCeiling.Record(-f.size, -1)
		bytes -= f.size
		objects--
		evicted++
		freed += f.size
	}
	log.Printf("cache store: evicted %d least recently read files (%d bytes) at its ceiling", evicted, freed)
}

func measureStore(ctx context.Context) {
	err := storeCeiling.ReconcileWith(ctx, func(ctx context.Context) (objectguard.Usage, error) {
		// safety: the bucket behind --blob-store is the controller's to measure.
		usage := objectguard.Usage{ObservedAt: time.Now().UTC()}
		for _, dir := range storeDirs() {
			bytes, files, partial, err := treeUsage(ctx, dir)
			usage.Bytes += bytes
			usage.Objects += files
			usage.Partial = usage.Partial || partial
			if err != nil {
				return objectguard.Usage{}, err
			}
		}
		return usage, nil
	})
	if err != nil {
		log.Printf("warning: measure cache store: %v", err)
	}
}

// perf: one walk per reconciliation interval, never per request, because each
// stored object already adds its own bytes to the running count. The walk stops
// when its context does, and says so, so a shutdown does not wait out a store
// with a million files in it.
func treeUsage(ctx context.Context, dir string) (bytes, files int64, partial bool, err error) {
	// #nosec G703 -- the roots come from the service's own configuration
	walkErr := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// safety: the check sits on directory entries, so a deep tree is abandoned
			// promptly without paying a context read per file.
			return ctx.Err()
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		files++
		return nil
	})
	switch {
	case walkErr == nil:
		return bytes, files, false, nil
	// safety: a tree the service has not created yet is empty, not a failed
	// measurement; anything else leaves the running count in place.
	case errors.Is(walkErr, fs.ErrNotExist):
		return bytes, files, false, nil
	case errors.Is(walkErr, context.Canceled), errors.Is(walkErr, context.DeadlineExceeded):
		return bytes, files, true, nil
	default:
		return 0, 0, false, fmt.Errorf("walk %s: %w", dir, walkErr)
	}
}

// safety: the walk outlives the request that asked for it, so a caller who hangs
// up does not abandon a measurement half way and leave the total wrong.
var (
	measureCtx  atomic.Pointer[context.Context]
	measureOnce objectguard.Coalescer
)

func setMeasureContext(ctx context.Context) { measureCtx.Store(&ctx) }

func serviceContext() context.Context {
	if ctx := measureCtx.Load(); ctx != nil {
		return *ctx
	}
	return context.Background()
}

// safety: a burst of admin calls costs one walk, and a call the running walk
// turned away still earns a repeat, because that walk may already have passed
// the directory the caller just emptied.
func measureStoreAsync() bool {
	return measureOnce.Go(func() { measureStore(serviceContext()) })
}

// safety: one shape for the ceiling wherever it is read, so the health route and
// the admin routes cannot drift apart.
func storeCeilingState() map[string]any {
	state := storeCeiling.State()
	out := map[string]any{
		"enforced":               state.Enforced,
		"frozen":                 state.Frozen,
		"warning":                state.Warning,
		"thawed":                 state.Thawed,
		"bytes":                  state.Bytes,
		"objects":                state.Objects,
		"max_bytes":              state.MaxBytes,
		"max_objects":            state.MaxObjects,
		"measurement_incomplete": state.Incomplete,
	}
	if !state.ReconciledAt.IsZero() {
		out["reconciled_at"] = state.ReconciledAt.UTC().Format(time.RFC3339)
	}
	return out
}

func storeCeilingProblems() []string {
	state := storeCeiling.State()
	if !state.Enforced {
		return nil
	}
	var problems []string
	switch {
	case state.Frozen:
		problems = append(problems, fmt.Sprintf(
			"store: the cache store is at its %s ceiling and uploads are refused", state.FrozenReason))
	case state.Warning:
		problems = append(problems, "store: the cache store is past its warning mark")
	}
	if state.Incomplete {
		problems = append(problems, "store: the last store measurement did not finish, so the total is the running count")
	}
	return problems
}

// safety: an operator who freed space, or who needs one more run out of a full
// volume, would otherwise wait out the reconciliation interval with no verb to
// reach; this service serves no delete of its own.
func handleStoreCeilingThaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	thawed, err := storeCeiling.Thaw()
	if err != nil {
		// safety: the refusal names the lever this service actually offers, because the
		// operator reading it cannot schedule a measurement without a restart.
		http.Error(w, err.Error()+
			". POST /admin/store-ceiling/measure walks the store now, which is what clears a freeze "+
			"once space has been freed", http.StatusConflict)
		return
	}
	writeStoreCeilingJSON(w, r, map[string]any{"thawed": thawed, "store_ceiling": storeCeilingState()})
}

// safety: measuring on demand is what turns a delete on the volume into uploads
// flowing again, rather than a wait for the end of the interval. The walk runs
// off the request, so its cost does not become the caller's latency and a
// disconnect cannot abandon it.
func handleStoreCeilingMeasure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	started := measureStoreAsync()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSONBody(w, r, map[string]any{
		"measuring":     true,
		"started":       started,
		"store_ceiling": storeCeilingState(),
	})
}

func writeStoreCeilingJSON(w http.ResponseWriter, r *http.Request, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONBody(w, r, body)
}

func initStoreCeilingMetrics() {
	meter := otelutil.Meter("sparkwing-cache")
	var failed error
	gauge := func(name, description, unit string) metric.Int64ObservableGauge {
		opts := []metric.Int64ObservableGaugeOption{metric.WithDescription(description)}
		if unit != "" {
			opts = append(opts, metric.WithUnit(unit))
		}
		g, err := meter.Int64ObservableGauge(name, opts...)
		failed = errors.Join(failed, err)
		return g
	}

	storeBytes := gauge("sparkwing.cache.store_bytes",
		"Bytes the cache store holds, counted per upload and replaced by each measurement", "By")
	storeObjects := gauge("sparkwing.cache.store_objects",
		"Files the cache store holds, counted per upload and replaced by each measurement", "{file}")
	ceilingBytes := gauge("sparkwing.cache.store_ceiling_bytes",
		"The configured byte ceiling; 0 while the store is unlimited", "By")
	ceilingObjects := gauge("sparkwing.cache.store_ceiling_objects",
		"The configured object ceiling; 0 while the count is unlimited", "{file}")
	frozen := gauge("sparkwing.cache.store_ceiling_frozen",
		"1 while the store is at its ceiling and uploads are refused", "")
	warning := gauge("sparkwing.cache.store_ceiling_warning",
		"1 while the store is past its warning mark", "")
	incomplete := gauge("sparkwing.cache.store_ceiling_measurement_incomplete",
		"1 while the last store measurement did not finish", "")
	refused := gauge("sparkwing.cache.store_ceiling_refused",
		"Uploads the store ceiling has refused", "{upload}")

	// safety: read at observation time from the ceiling itself, so the gauges cannot
	// drift from the state the health route and the refusals report.
	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		state := storeCeiling.State()
		o.ObserveInt64(storeBytes, state.Bytes)
		o.ObserveInt64(storeObjects, state.Objects)
		o.ObserveInt64(ceilingBytes, state.MaxBytes)
		o.ObserveInt64(ceilingObjects, state.MaxObjects)
		o.ObserveInt64(frozen, boolGauge(state.Frozen))
		o.ObserveInt64(warning, boolGauge(state.Warning))
		o.ObserveInt64(incomplete, boolGauge(state.Incomplete))
		o.ObserveInt64(refused, int64(state.Refused))
		return nil
	}, storeBytes, storeObjects, ceilingBytes, ceilingObjects, frozen, warning, incomplete, refused)
	if failed = errors.Join(failed, err); failed != nil {
		log.Printf("warning: store ceiling metrics unavailable, so the ceiling shows only on /health: %v", failed)
	}
}

func boolGauge(b bool) int64 {
	if b {
		return 1
	}
	return 0
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

// safety: an overwrite adds a size difference and no object, and existence
// rather than size decides that, because an empty file is still a file.
func storeDelta(path string, written int64) (bytes, objects int64) {
	// #nosec G703 -- callers pass a path they validated and have just written
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return written, 1
	}
	return written - info.Size(), 0
}
