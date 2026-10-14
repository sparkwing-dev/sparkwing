// Package runflags is the one encoding of the run flags --sw-start-at,
// --sw-stop-at, --sw-only, --sw-dry-run and --sw-no-cache: the trigger_env
// keys `sparkwing pipeline trigger` carries them in, and the keys of a run
// row's invocation "flags" map, which every host of the run reads them from.
package runflags

// Flags are a run's step range, node filter, dry run and memo bypass.
type Flags struct {
	StartAt, StopAt, Only string
	DryRun, NoCache       bool
}

// The trigger_env keys a submitted trigger carries the flags in.
const (
	EnvStartAt = "SPARKWING_START_AT"
	EnvStopAt  = "SPARKWING_STOP_AT"
	EnvOnly    = "SPARKWING_ONLY"
	EnvDryRun  = "SPARKWING_DRY_RUN"
	EnvNoCache = "SPARKWING_NO_CACHE"
)

// FromTriggerEnv reads the flags from a trigger's environment.
func FromTriggerEnv(env map[string]string) Flags {
	return Flags{
		StartAt: env[EnvStartAt], StopAt: env[EnvStopAt], Only: env[EnvOnly],
		DryRun: env[EnvDryRun] == "1", NoCache: env[EnvNoCache] == "1",
	}
}

// Record writes the flags that are set into an invocation's flags map.
func (f Flags) Record(flags map[string]any) {
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

// Invocation is a run row's invocation holding only the flags, or nil when
// none is set.
func (f Flags) Invocation() map[string]any {
	flags := map[string]any{}
	f.Record(flags)
	if len(flags) == 0 {
		return nil
	}
	return map[string]any{"flags": flags}
}

// FromInvocation reads the flags a run row's invocation records.
func FromInvocation(invocation map[string]any) Flags {
	flags, _ := invocation["flags"].(map[string]any)
	text := func(key string) string { v, _ := flags[key].(string); return v }
	set := func(key string) bool { v, _ := flags[key].(bool); return v }
	return Flags{
		StartAt: text("start_at"), StopAt: text("stop_at"), Only: text("only"),
		DryRun: set("dry_run"), NoCache: set("no_cache"),
	}
}

// DefaultBranchKey is the run row invocation key that holds the repository's
// default branch a GitHub App trigger recorded, which git:branch=default
// guards compare the run's branch with.
const DefaultBranchKey = "git_default_branch"
