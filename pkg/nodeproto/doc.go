// Package nodeproto defines protocol v2 requests, frames and strict declarations.
package nodeproto

import "github.com/sparkwing-dev/sparkwing/pkg/limits"

// MaxLineBytes bounds the payload and its framing using the common limits owner.
const MaxLineBytes = int(limits.NodeOutputBytes + limits.ProtocolEnvelopeBytes)
