package sparkwing

import "github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"

// ErrIsolatedHomeRequired refuses discovery before a pipeline can open an
// operational store. Return or wrap it from Plan when a temporary home is required.
// Hosts show fixed recovery guidance even before secret declarations are known.
var ErrIsolatedHomeRequired error = apierr.IsolatedHomeRefusal()
