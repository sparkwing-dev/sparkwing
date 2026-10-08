//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package secrets

import "os"

// safety: without a way to pass an extra descriptor or count a pipe's bytes, a
// pipeline started here keeps only its own in-process masking.
const valuesChannelSupported = false

func inheritedValuesFile(int) *os.File { return nil }

func (v *ChildValues) pump() {}

func (v *ChildValues) pending() int { return 0 }
