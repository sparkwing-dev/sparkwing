package cluster

import "time"

// safety: a Retry-After the server rounded to nothing would otherwise spin a
// heartbeat loop, so every shed beat waits at least this long.
const minShedBackoff = time.Second
