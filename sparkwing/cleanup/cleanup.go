// Package cleanup lets a sparks library register best-effort cleanup for
// resources outside the step's process tree. The host runs unreleased
// cleanups when the node process exits. Registration requires a host connection.
//
// Core reaps the processes a step spawns on its own, with no registration.
// Register is only for resources that live outside the step's process tree,
// in another daemon or another system, which core cannot see.
package cleanup

import (
	"context"
	"fmt"
	"time"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

const releaseTimeout = 10 * time.Second

// Spec describes a cleanup to run if the registering node dies first.
type Spec struct {
	// Argv is the command that tears the resource down, run directly with
	// no shell. It MUST be idempotent -- exit zero whether or not the
	// resource still exists -- because the reaper runs it once, in a
	// process whose environment may differ from the step's, and does not
	// retry. Empty Argv makes Register a no-op.
	Argv []string
	// Description is a safe resource label for cleanup diagnostics. It must
	// contain no secrets. Orphan-sweep events use a fixed label when empty.
	Description string
}

// Register retains cleanup in the bound host until the returned release is called.
// The host runs unreleased commands after the SDK process exits.
func Register(ctx context.Context, spec Spec) (release func(), err error) {
	sdkserve.RequireExecution(ctx, "cleanup.Register")
	if len(spec.Argv) == 0 {
		return func() {}, nil
	}
	c, ok := sdkserve.ServicesFrom(ctx).(cleanupHost)
	if !ok {
		return func() {}, fmt.Errorf("host cleanup service unavailable")
	}
	return registerWithHost(ctx, c, spec)
}

func registerWithHost(ctx context.Context, c cleanupHost, spec Spec) (func(), error) {
	if c == nil || len(spec.Argv) == 0 {
		return func() {}, nil
	}
	id, err := c.RegisterCleanup(ctx, spec.Argv, spec.Description)
	if err != nil {
		return func() {}, err
	}
	// safety: a release runs after the library cleaned up, often once the
	// step's context has ended; a release that fails only costs a second run
	// of an idempotent argv.
	return func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		_ = c.ReleaseCleanup(rctx, id) //nolint:errcheck // a failed release only reruns an idempotent argv
	}, nil
}

type cleanupHost interface {
	RegisterCleanup(context.Context, []string, string) (string, error)
	ReleaseCleanup(context.Context, string) error
}
