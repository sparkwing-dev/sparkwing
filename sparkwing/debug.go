package sparkwing

import "context"

// Debug emits a debug record; the host decides whether to retain it.
func Debug(ctx context.Context, format string, args ...any) { emitLevel(ctx, "debug", format, args...) }
