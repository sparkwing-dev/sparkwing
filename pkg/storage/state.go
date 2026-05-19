package storage

import "github.com/sparkwing-dev/sparkwing/pkg/store"

// StateStore is the run-record store: runs, nodes, steps, annotations,
// approvals, concurrency, and the schema migrations the orchestrator
// depends on.
//
// State is opened from a backends.Spec via
// pkg/storage/storeurl.OpenStateStoreFromSpec. The handle is the same
// concrete *store.Store the orchestrator has always held; declaring it
// here gives the factory and SDK alias a typed name without imposing a
// minimal-method-set interface that *store.Store's broad surface
// wouldn't fit cleanly.
//
// Implementations today: sqlite. Recognized but not implemented in
// this build: postgres, mysql, controller.
type StateStore = *store.Store
