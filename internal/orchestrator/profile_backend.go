package orchestrator

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: keepState is set by a run whose state already reaches this
// machine's store through the admission daemon, or that holds the standalone
// store the fallback chose. Opening the state again would take a file that
// run does not own.
func resolveLocalBackends(opts *Options, keepState bool) error {
	if opts.State == nil && !keepState {
		if opts.DefaultStateDB == "" {
			return fmt.Errorf("state backend: no state database path resolved")
		}
		st, err := storeOpen(opts.DefaultStateDB)
		if err != nil {
			return fmt.Errorf("state backend: open %s: %w", opts.DefaultStateDB, err)
		}
		opts.State = st
	}
	if opts.State == nil {
		return fmt.Errorf("state backend: no store resolved")
	}
	return nil
}

// hack: an indirection so a test can count every store a run opens, whichever
// path opened it.
var storeOpen = store.Open
