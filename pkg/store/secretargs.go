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

// IncludeSecretValues is the `?include=` token a caller passes to
// GET /api/v1/runs/{id} to receive a run's real argument values rather
// than the redacted view.
//
// It exists because the run API is not only a display surface: a
// cluster executor fetches the args it is about to run with from the
// same endpoint the dashboard reads. Redaction that cannot be opted
// out of would make pods execute with a literal "***" and, worse,
// would stop the node-log masker from recognizing the real secret --
// turning a display leak into a log leak.
//
// The controller honors it only for principals that can execute work
// (nodes.claim or admin); everyone else, the dashboard included, keeps
// getting the redacted view. Lives here rather than in the controller
// or its client so the server and the caller cannot drift on the
// spelling.
const IncludeSecretValues = "secret_values"

// SecretArgNames returns the arg names this run declared secret, or
// nil when the run carries no classification.
//
// Nil means "nothing to redact", which covers two cases this code
// cannot tell apart and does not need to: a pipeline that declares no
// secret inputs, and a run written before [InvocationSecretArgsKey]
// existed. The second is the grandfathered one -- those rows render
// exactly as they did before, because reclassifying them would need a
// schema no read path can reach.
func (r Run) SecretArgNames() []string {
	if len(r.Invocation) == 0 {
		return nil
	}
	return decodeStringSlice(r.Invocation[InvocationSecretArgsKey])
}

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

// RedactedForDisplay returns a value safe to render to a human, to
// JSON, or over HTTP: every arg the run declared secret has its value
// replaced by [RedactedArgValue] in Args, in Invocation["args"], and in
// the Invocation["reproducer"] command line.
//
// This is a read-path transform only. The stored row keeps the
// plaintext because the log masker derives its redaction set from it
// and retry re-executes with it -- never route a re-execution path
// through this function. Storage-at-rest is a separate decision.
//
// Treat the result as read-only. Redacted fields are always freshly
// allocated, so writing to them cannot corrupt the source; every other
// field, and the whole value when there is nothing to redact, is
// shared with r. Rendering never writes, so nothing needs the deeper
// copy, and the no-secrets path stays allocation-free.
func (r Run) RedactedForDisplay() Run {
	names := r.SecretArgNames()
	if len(names) == 0 {
		return r
	}
	secret := make(map[string]struct{}, len(names))
	for _, n := range names {
		secret[n] = struct{}{}
	}

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

// RedactedRun returns a pointer to a redacted view of run, the shape
// every handler and renderer holds. Nil-safe.
//
// Never mutates the source, whose Args a retry or a trigger claim may
// be about to re-execute from. See [Run.RedactedForDisplay] for what
// the result shares with it and why that is safe to render.
func RedactedRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	redacted := run.RedactedForDisplay()
	return &redacted
}

// InheritSecretArgs copies src's secret-arg classification onto inv,
// returning inv (allocating it when nil) so a run minted from another
// run redacts the same args as the run it came from.
//
// Retry and replay build their row straight from a source run rather
// than through the orchestrator's invocation snapshot, so without this
// the new row carries the same plaintext args with no classification
// and renders them in the clear -- the original defect, reintroduced
// one hop downstream. The classification is the only thing copied; the
// orchestrator overwrites the whole invocation with the real snapshot
// when the run actually starts.
//
// Returns inv untouched when src carries no classification.
func InheritSecretArgs(inv map[string]any, src *Run) map[string]any {
	if src == nil {
		return inv
	}
	names := src.SecretArgNames()
	if len(names) == 0 {
		return inv
	}
	if inv == nil {
		inv = make(map[string]any, 1)
	}
	inv[InvocationSecretArgsKey] = names
	return inv
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
