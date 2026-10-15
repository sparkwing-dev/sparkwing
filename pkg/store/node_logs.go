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
