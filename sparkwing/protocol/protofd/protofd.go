// Package protofd finds the node protocol's request and frame pipes and makes
// them non-inheritable while packages initialize. The Go SDK imports it, so it
// runs before the init of any package that imports the SDK and a command such
// an init starts never holds the pipes. A package that does not import the SDK
// is not ordered after it, so its init can still start a command first
// (docs/node-protocol.md, Close-on-exec).
//
// It imports only the standard library, which is what lets the SDK import it
// without being initialized after anything else.
package protofd

import (
	"errors"
	"os"
)

// Arg is the one argument a host starts an SDK process with.
const Arg = "--sw-node-protocol"

var (
	req, frames *os.File
	openErr     error
)

func init() {
	if len(os.Args) < 2 || os.Args[1] != Arg {
		openErr = errors.New("the process was not started with " + Arg)
		return
	}
	req, frames, openErr = open()
}

// Pipes returns the request and frame pipes found at initialization, or why
// there are none.
func Pipes() (request, frame *os.File, err error) {
	return req, frames, openErr
}
