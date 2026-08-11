package store

import (
	"sort"
	"strings"
)

// InvocationSecretArgsKey names the [Run.Invocation] entry listing the
// arg names a run's pipeline declared `secret:"true"`. The orchestrator
// writes it at run start from the pipeline's input schema.
//
// It exists because a run row outlives the process that registered its
// pipeline: `sparkwing runs get` on a laptop, the controller serving
// the dashboard, and a receipt recomputed months later all read args
// out of the database with no schema to consult. Recording the
// classification alongside the args is what lets those read paths
// redact without guessing.
const InvocationSecretArgsKey = "secret_args"

// RedactedArgValue replaces a secret arg's value on every read path.
// Matches the placeholder the log masker writes so operators see one
// consistent marker.
const RedactedArgValue = "***"

// SecretArgNames returns the arg names this run declared secret, or
// nil when the run carries no classification.
//
// Nil is the grandfathered case, not an assertion that nothing is
// secret: runs written before [InvocationSecretArgsKey] existed have
// no list, and they render exactly as they did before -- redacting
// them would need a schema this code cannot reach. Runs written since
// carry the list even when it is empty, so absent and "no secrets"
// stay distinguishable in the stored JSON.
func (r Run) SecretArgNames() []string {
	if len(r.Invocation) == 0 {
		return nil
	}
	return decodeStringSlice(r.Invocation[InvocationSecretArgsKey])
}

// decodeStringSlice reads a []string that may have round-tripped
// through JSON as []any. The in-process orchestrator path hands over
// the original []string; anything read back out of the database or off
// the wire arrives as []any.
func decodeStringSlice(v any) []string {
	switch typed := v.(type) {
	case []string:
		if len(typed) == 0 {
			return nil
		}
		out := make([]string, 0, len(typed))
		out = append(out, typed...)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// RedactedForDisplay returns a copy of r safe to render to a human, to
// JSON, or over HTTP: every arg the run declared secret has its value
// replaced by [RedactedArgValue] in Args, in Invocation["args"], and in
// the Invocation["reproducer"] command line.
//
// This is a read-path transform only. The stored row keeps the
// plaintext because the log masker derives its redaction set from it
// and retry re-executes with it -- never route a re-execution path
// through this function. Storage-at-rest is a separate decision.
//
// Returns r unchanged when the run declares no secret args, so the
// common case allocates nothing.
func (r Run) RedactedForDisplay() Run {
	names := r.SecretArgNames()
	if len(names) == 0 {
		return r
	}
	secret := make(map[string]struct{}, len(names))
	for _, n := range names {
		secret[n] = struct{}{}
	}

	// Collect the plaintext values before overwriting them so the
	// reproducer rewrite still knows what to look for.
	values := make(map[string]string, len(names))
	for k, v := range r.Args {
		if _, ok := secret[k]; ok && v != "" {
			values[k] = v
		}
	}

	out := r
	if len(r.Args) > 0 {
		args := make(map[string]string, len(r.Args))
		for k, v := range r.Args {
			if _, ok := secret[k]; ok && v != "" {
				v = RedactedArgValue
			}
			args[k] = v
		}
		out.Args = args
	}
	out.Invocation = redactInvocation(r.Invocation, secret, values)
	return out
}

// RedactInvocation returns a copy of an invocation snapshot with the
// values of its secret-declared args replaced. Exported for emit paths
// that hold the map before a Run exists -- the orchestrator's
// run_start envelope in particular, whose attrs are the same snapshot.
//
// Returns inv unchanged when it carries no secret-arg classification.
func RedactInvocation(inv map[string]any) map[string]any {
	names := decodeStringSlice(inv[InvocationSecretArgsKey])
	if len(names) == 0 {
		return inv
	}
	secret := make(map[string]struct{}, len(names))
	for _, n := range names {
		secret[n] = struct{}{}
	}
	values := make(map[string]string, len(names))
	for k, v := range invocationArgs(inv) {
		if _, ok := secret[k]; ok && v != "" {
			values[k] = v
		}
	}
	return redactInvocation(inv, secret, values)
}

// redactInvocation rewrites the args sub-map and the reproducer
// command. The copy is shallow apart from those two entries: every
// other key (flags, hints, profile, backends, hashes) is carried over
// by reference, which is safe because callers treat the result as
// read-only and the redacted entries are the only ones replaced.
func redactInvocation(inv map[string]any, secret map[string]struct{}, values map[string]string) map[string]any {
	if len(inv) == 0 {
		return inv
	}
	args := invocationArgs(inv)
	repro, _ := inv["reproducer"].(string)
	if len(args) == 0 && repro == "" {
		return inv
	}

	out := make(map[string]any, len(inv))
	for k, v := range inv {
		out[k] = v
	}
	if len(args) > 0 {
		// Any value present here but not in Run.Args still needs
		// covering -- the two maps are written together but a
		// hand-built invocation could disagree.
		redacted := make(map[string]string, len(args))
		for k, v := range args {
			if _, ok := secret[k]; ok && v != "" {
				if _, known := values[k]; !known {
					values[k] = v
				}
				v = RedactedArgValue
			}
			redacted[k] = v
		}
		out["args"] = redacted
	}
	if repro != "" {
		out["reproducer"] = redactReproducer(repro, values)
	}
	return out
}

// redactReproducer rewrites `--name=value` to `--name=***` for each
// secret arg. The match is anchored on the flag name rather than on
// the bare value so a secret that happens to equal a common token
// ("true", "1") cannot shred unrelated parts of the command.
//
// Longest value first so a secret that is a prefix of another does not
// leave a tail behind.
func redactReproducer(repro string, values map[string]string) string {
	if len(values) == 0 {
		return repro
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(values[names[i]]) != len(values[names[j]]) {
			return len(values[names[i]]) > len(values[names[j]])
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		repro = strings.ReplaceAll(repro,
			"--"+name+"="+values[name],
			"--"+name+"="+RedactedArgValue)
	}
	return repro
}

// invocationArgs reads the invocation's args sub-map, which arrives as
// map[string]string in process and map[string]any after a JSON round
// trip.
func invocationArgs(inv map[string]any) map[string]string {
	switch typed := inv["args"].(type) {
	case map[string]string:
		return typed
	case map[string]any:
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
}

// RedactedRun returns a pointer to a redacted copy of run, the shape
// every handler and renderer holds. Nil-safe.
//
// A copy, never a mutation: the caller's *Run may be the same value a
// retry or a trigger claim is about to re-execute from.
func RedactedRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	redacted := run.RedactedForDisplay()
	return &redacted
}

// RedactedRuns applies [RedactedRun] across a slice -- the shape every
// list endpoint and `runs list` renderer works in. Preserves nil vs
// empty so JSON encoders keep emitting `[]` where they did before.
func RedactedRuns(runs []*Run) []*Run {
	if runs == nil {
		return nil
	}
	out := make([]*Run, len(runs))
	for i, r := range runs {
		out[i] = RedactedRun(r)
	}
	return out
}
