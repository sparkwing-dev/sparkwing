//go:build !windows

package store

import "syscall"

// safety: a FIFO swapped in at a sidecar path must not block the open.
const sqliteTightenFlags = syscall.O_NONBLOCK
