package store

import "fmt"

// LiveLogAttempt identifies one attempt's live stream.
func LiveLogAttempt(attempt, generation int64) string {
	return fmt.Sprintf("a%d-g%d", attempt, generation)
}
