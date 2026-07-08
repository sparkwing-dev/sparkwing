package client

import "github.com/sparkwing-dev/sparkwing/pkg/storage"

// Compile-time check: *Client satisfies storage.StateStore. The
// orchestrator's StateBackend (which adds AppendEvent, GetNodeOutput,
// EnqueueTrigger on top) is asserted next to its declaration in
// internal/orchestrator/backends.go to avoid importing orchestrator
// here.
var _ storage.StateStore = (*Client)(nil)
