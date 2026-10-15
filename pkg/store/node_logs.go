package store

import (
	"fmt"
	"time"
)

// NodeLogSealGrace bounds final uploads from an ended legacy claim.
const NodeLogSealGrace = 60 * time.Second

// LiveLogOffsetHeader names the byte offset in a node's whole log at
// which a live log append begins. A writer that keeps its log as
// segments sends it, so a reader can join the live view to the segments.
const LiveLogOffsetHeader = "X-Sparkwing-Log-Offset"

// LiveLogAttemptHeader names, beside [LiveLogOffsetHeader], the attempt
// whose bytes the offsets count, as "a<attempt>-g<generation>". Each
// attempt numbers its log from zero, so a new attempt starts the live
// view over.
const LiveLogAttemptHeader = "X-Sparkwing-Log-Attempt"

// LiveLogAttempt is the [LiveLogAttemptHeader] value for one attempt.
func LiveLogAttempt(attempt, generation int64) string {
	return fmt.Sprintf("a%d-g%d", attempt, generation)
}

// LogWatchHeader is the request header that, sent as "1" on a node claim
// or run heartbeat, asks the controller to answer which of the run's
// nodes a viewer is watching. A heartbeat without it is answered 204.
const LogWatchHeader = "X-Sparkwing-Log-Watch"

// LogSegmentRequest asks the controller to sign the upload of one sealed
// segment of a node attempt's log, for exactly its size and digests. N
// zero signs nothing and names the attempt the writer writes. Attempt and
// Generation echo that answer, so a writer whose attempt was superseded
// is refused; both are zero on the first request.
type LogSegmentRequest struct {
	Attempt    int64 `json:"attempt,omitempty"`
	Generation int64 `json:"generation,omitempty"`
	N          int   `json:"n"`
	// Size is the stored (compressed) segment's length in bytes.
	Size int64 `json:"size,omitempty"`
	// MD5 is the stored segment's MD5, base64, as Content-MD5 carries it.
	MD5 string `json:"md5,omitempty"`
	// SHA256 is the stored segment's SHA-256, hex, as the manifest lists it.
	SHA256 string `json:"sha256,omitempty"`
}

// LogSegmentGrant answers a [LogSegmentRequest]: the attempt the writer
// writes, its key prefix, and for a segment one presigned PUT that sends
// Headers and accepts only the signed bytes until ExpiresAt.
type LogSegmentGrant struct {
	Attempt    int64             `json:"attempt"`
	Generation int64             `json:"generation"`
	Prefix     string            `json:"prefix"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at,omitzero"`
	// Watched reports that a viewer has the node's log open, so the
	// writer pushes its lines to the live view.
	Watched bool `json:"watched"`
}
