package limits

import "time"

// AuthCacheTTL bounds cached credential facts; generation equality is also required on every reuse.
const AuthCacheTTL time.Duration = 5 * time.Second
