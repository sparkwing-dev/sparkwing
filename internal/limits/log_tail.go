package limits

import "time"

// LogSealGrace preserves the final attempt's existing post-result log flush window.
// It never extends credential expiry or authorizes a newer attempt or generation.
const LogSealGrace time.Duration = time.Minute

// RetryLogSealWait bounds how long a retry holds its commit for the previous attempt's seal.
// A retry advances the log writer fence, so a tail that lands later is refused.
const RetryLogSealWait time.Duration = 5 * time.Second

// KeeperDiagnosticSealWait fits inside the existing retry-tail fence; diagnostics never extend it.
const KeeperDiagnosticSealWait time.Duration = 100 * time.Millisecond
