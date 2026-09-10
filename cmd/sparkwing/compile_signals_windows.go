//go:build windows

package main

import "os"

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// safety: Windows has no way to re-raise a caught signal with its default
// action, so an interrupted compile returns its error and the CLI reports it.
func reraise(os.Signal) {}
