package store

import (
	"fmt"
	"time"
)

// NodeLogSealGrace bounds final uploads from an ended legacy claim.
const NodeLogSealGrace = 60 * time.Second

// LiveLogAttempt identifies one attempt's live stream.
func LiveLogAttempt(attempt, generation int64) string {
	return fmt.Sprintf("a%d-g%d", attempt, generation)
}
