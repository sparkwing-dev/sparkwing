// Package runflags is the one encoding of the run flags --sw-start-at,
// --sw-stop-at, --sw-only, --sw-dry-run and --sw-no-cache: the trigger_env
// keys `sparkwing pipeline trigger` carries them in, and the keys of a run
// row's invocation "flags" map, which every host of the run reads them from.
package runflags

// flags are a run's step range, node filter, dry run and memo bypass.
type flags struct {
	StartAt, StopAt, Only string
	DryRun, NoCache       bool
}

// The trigger_env keys a submitted trigger carries the flags in.
const (
	envStartAt = "SPARKWING_START_AT"
	envStopAt  = "SPARKWING_STOP_AT"
	envOnly    = "SPARKWING_ONLY"
	envDryRun  = "SPARKWING_DRY_RUN"
	envNoCache = "SPARKWING_NO_CACHE"
)

// record writes the flags that are set into an invocation's flags map.
func (f flags) record(flags map[string]any) {
	for key, value := range map[string]string{"start_at": f.StartAt, "stop_at": f.StopAt, "only": f.Only} {
		if value != "" {
			flags[key] = value
		}
	}
	if f.DryRun {
		flags["dry_run"] = true
	}
	if f.NoCache {
		flags["no_cache"] = true
	}
}
