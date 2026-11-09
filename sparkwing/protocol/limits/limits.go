// Package limits owns bounds carried by the SDK transport.
package limits

import "time"

const (
	// NodeOutputBytes bounds typed output per job or step.
	NodeOutputBytes int64 = 16 << 20
	// ProtocolEnvelopeBytes allows framing beyond a typed output.
	ProtocolEnvelopeBytes int64 = 1 << 20
	// ChildAwait bounds a child status long poll.
	ChildAwait time.Duration = 60 * time.Second
)
