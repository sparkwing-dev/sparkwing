package limits

import "time"

// LogSealGrace preserves the final attempt's existing post-result log flush window.
// It never extends credential expiry or authorizes a newer attempt or generation.
const LogSealGrace time.Duration = time.Minute
