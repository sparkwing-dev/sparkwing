// Package nodeproto is the wire format of the node protocol between the
// engine's node host and an SDK process started with --sw-node-protocol
// (docs/node-protocol.md).
//
// The host writes one [Request] per line to the request pipe. The SDK writes
// one frame per line to the frame pipe: a [Reply] to a request or a
// [LogRecord]; [ParseFrame] decodes it. Pipeline code's stdout and stderr are
// separate streams the host captures as text, so nothing pipeline code prints
// can pass for a frame. A [Tracker] matches replies to the requests the host
// sent, and a [Writer] serializes concurrent writers so lines never
// interleave.
package nodeproto

// Version is the node protocol version this build speaks. Host and SDK match
// it exactly; a mismatch is refused rather than negotiated.
const Version = "1"

// MaxOutputBytes is the largest step output value, as JSON, that an SDK may
// produce and the host records. It bounds every step's output, not only the
// one that travels as the node's output.
const MaxOutputBytes = 16 << 20

// MaxLineBytes is the longest line, newline included, that a [Reader]
// accepts and a [Writer] sends. A reply carries a whole describe document,
// plan document or node output; the bound leaves room for a reply's own
// fields around an output of [MaxOutputBytes], and exists so a process that
// never writes a newline cannot grow the host's memory without limit.
const MaxLineBytes = MaxOutputBytes + 1<<20
