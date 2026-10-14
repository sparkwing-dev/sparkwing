// Package cleanup lets a sparks library guarantee that a resource it starts
// -- a container, a cluster, a release -- is torn down if the step's node
// dies before the library's own cleanup runs. It is the extension point that
// keeps such resource types out of the core SDK: the library says how to
// clean up (a command), and sparkwing runs that command from the same sweeps
// that reap step processes (before each run, in the admission daemon when a
// run's connection drops, and in `sparkwing doctor`).
//
// Core reaps the processes a step spawns on its own, with no registration.
// Register is only for resources that live outside the step's process tree,
// in another daemon or another system, which core cannot see.
package cleanup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
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
	// Description names the resource for the reaped event and for
	// `sparkwing doctor`; defaults to the command's program name when empty.
	Description string
}

// Register records a cleanup that runs if the registering process exits
// before the library calls the release it returns. In a process the engine
// runs over the node protocol, the engine holds the cleanup and runs
// Spec.Argv once that process exits, unless it was released. A tool a step
// starts carries the node's run and node ids but not its bearer, so it
// records the cleanup for a later sweep to run. Outside a pipeline node --
// no run and node in the environment -- there is nothing to reap for, so
// Register is a no-op returning a no-op release.
func Register(ctx context.Context, spec Spec) (release func(), err error) {
	if c, bound := nodeclient.Bound(); bound {
		return registerWithHost(ctx, c, spec)
	}
	run := os.Getenv("SPARKWING_RUN_ID")
	node := os.Getenv("SPARKWING_NODE_ID")
	if run == "" || node == "" || len(spec.Argv) == 0 {
		return func() {}, nil
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return func() {}, err
	}
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		return func() {}, err
	}
	// safety: a tool a step starts holds no masker, so an unnamed cleanup is
	// recorded by its program, never by arguments that can quote a secret.
	desc := spec.Description
	if desc == "" {
		desc = filepath.Base(spec.Argv[0])
	}
	var idb [8]byte
	_, _ = rand.Read(idb[:])
	rel, err := sessionledger.Open(p.SessionLedgerDir()).Record(sessionledger.Record{
		Run:        run,
		Node:       node,
		OwnerPID:   os.Getpid(),
		OwnerBirth: birth,
		Handle:     sessionledger.Handle{Kind: "command", ID: hex.EncodeToString(idb[:]), Argv: spec.Argv},
		Command:    sessionledger.MaskCommand(ctx, desc),
	})
	if err != nil {
		return func() {}, err
	}
	return rel, nil
}

func registerWithHost(ctx context.Context, c *nodeclient.Client, spec Spec) (func(), error) {
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
