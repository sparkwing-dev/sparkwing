// Package nodeproto is the wire format of the node protocol between the
// engine's node host and an SDK process started with --sw-node-protocol
// (docs/node-protocol.md).
//
// The host writes one [Request] per line to the process's stdin. The process
// writes NDJSON to stdout, where each line is a [Reply] to a request, a
// [LogRecord], or plain text that pipeline code printed; [Classify] tells them
// apart. A [Tracker] matches replies to the requests the host sent, and a
// [Writer] serializes concurrent writers so lines never interleave.
package nodeproto

// Version is the node protocol version this build speaks. Host and SDK match
// it exactly; a mismatch is refused rather than negotiated.
const Version = "1"

// MaxLineBytes is the longest line, without its newline, that a [Reader]
// accepts and a [Writer] sends. A reply carries a whole describe document,
// plan document or node output, so the bound is generous; it exists so a
// process that never writes a newline cannot grow the host's memory without
// limit.
const MaxLineBytes = 16 << 20
