//go:build !windows

package artifacts

import "syscall"

const artifactNonblock = syscall.O_NONBLOCK
