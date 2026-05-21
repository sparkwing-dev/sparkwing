package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithStepRange installs a --start-at / --stop-at range on ctx so
// every Work executed under it can filter its items down to the
// selected window. The orchestrator validates the strings before
// installing -- RunWork applies the filter only to Works that
// actually contain the named bound, so multi-Job pipelines that
// pass the range globally degrade gracefully on Works that don't.
//
// Lets `sparkwing run <pipeline> --start-at STEP` skip every step upstream
// of STEP and resume from there without authors having to hand-roll
// a stepOrder slice + skipBefore predicate per pipeline.
func WithStepRange(ctx context.Context, startAt, stopAt string) context.Context {
	if startAt == "" && stopAt == "" {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.StepRange, [2]string{startAt, stopAt})
}

// StepRangeFromContext returns the (startAt, stopAt) bounds plumbed
// onto ctx by WithStepRange. Both empty when no range was set.
// Exported so renderers (e.g. `sparkwing pipeline explain`) can
// preview "what would be skipped" without re-running RunWork.
func StepRangeFromContext(ctx context.Context) (startAt, stopAt string) {
	v, _ := ctx.Value(sparkwing.RuntimePlumbing.Keys.StepRange).([2]string)
	return v[0], v[1]
}
