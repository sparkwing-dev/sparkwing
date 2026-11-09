// Package nodeproto defines protocol v2 requests, frames and strict declarations.
package nodeproto

import protolimits "github.com/sparkwing-dev/sparkwing/sparkwing/protocol/limits"

// MaxLineBytes bounds the payload and its framing using the common limits owner.
const MaxLineBytes = int(protolimits.NodeOutputBytes + protolimits.ProtocolEnvelopeBytes)
