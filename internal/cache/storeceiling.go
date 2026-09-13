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
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeStoreCeilingJSON(w, r, map[string]any{"thawed": thawed, "store_ceiling": storeCeilingState()})
}

// safety: measuring on demand is what turns a delete on the volume into uploads
// flowing again, rather than a wait for the end of the interval.
func handleStoreCeilingMeasure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	measureStore(r.Context())
	writeStoreCeilingJSON(w, r, map[string]any{"store_ceiling": storeCeilingState()})
}

func writeStoreCeilingJSON(w http.ResponseWriter, r *http.Request, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONBody(w, r, body)
}

func initStoreCeilingMetrics() {
	meter := otelutil.Meter("sparkwing-cache")

	storeBytes, _ := meter.Int64ObservableGauge("sparkwing.cache.store_bytes",
		metric.WithDescription("Bytes the cache store holds, counted per upload and replaced by each measurement"),
		metric.WithUnit("By"))
	storeObjects, _ := meter.Int64ObservableGauge("sparkwing.cache.store_objects",
		metric.WithDescription("Files the cache store holds, counted per upload and replaced by each measurement"),
		metric.WithUnit("{file}"))
	storeCeilingBytes, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_bytes",
		metric.WithDescription("The configured byte ceiling; 0 while the store is unlimited"),
		metric.WithUnit("By"))
	storeCeilingObjects, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_objects",
		metric.WithDescription("The configured object ceiling; 0 while the count is unlimited"),
		metric.WithUnit("{file}"))
	frozen, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_frozen",
		metric.WithDescription("1 while the store is at its ceiling and uploads are refused"))
	warning, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_warning",
		metric.WithDescription("1 while the store is past its warning mark"))
	incomplete, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_measurement_incomplete",
		metric.WithDescription("1 while the last store measurement did not finish"))
	refused, _ := meter.Int64ObservableGauge("sparkwing.cache.store_ceiling_refused",
		metric.WithDescription("Uploads the store ceiling has refused"),
		metric.WithUnit("{upload}"))

	// safety: read at observation time from the ceiling itself, so the gauges cannot
	// drift from the state the health route and the refusals report.
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		state := storeCeiling.State()
		o.ObserveInt64(storeBytes, state.Bytes)
		o.ObserveInt64(storeObjects, state.Objects)
		o.ObserveInt64(storeCeilingBytes, state.MaxBytes)
		o.ObserveInt64(storeCeilingObjects, state.MaxObjects)
		o.ObserveInt64(frozen, boolGauge(state.Frozen))
		o.ObserveInt64(warning, boolGauge(state.Warning))
		o.ObserveInt64(incomplete, boolGauge(state.Incomplete))
		o.ObserveInt64(refused, int64(state.Refused))
		return nil
	}, storeBytes, storeObjects, storeCeilingBytes, storeCeilingObjects, frozen, warning, incomplete, refused)
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
