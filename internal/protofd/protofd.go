// Package protofd finds the node protocol's request and frame pipes and makes
// them non-inheritable while packages initialize, and takes the node routes'
// bearer out of the environment at the same point. The Go SDK imports it, so it
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

// HandlesEnv names the inherited pipe handles on Windows, as
// "<request>,<frame>" in decimal. Unix uses descriptors 3 and 4.
const HandlesEnv = "SPARKWING_PROTOCOL_HANDLES"

// NodeURLEnv and NodeTokenEnv name the node routes' base URL and bearer.
const (
	NodeURLEnv   = "SPARKWING_NODE_URL"
	NodeTokenEnv = "SPARKWING_NODE_TOKEN" // #nosec G101 -- the variable's name, not its value
)

var (
	req, frames         *os.File
	openErr             error
	nodeURL, nodeBearer string
)

func init() {
	if len(os.Args) != 2 || os.Args[1] != Arg {
		openErr = errors.New("the process was not started with " + Arg)
		return
	}
	// safety: removed before any later init can start a command that would
	// inherit the bearer.
	nodeURL, nodeBearer = os.Getenv(NodeURLEnv), os.Getenv(NodeTokenEnv)
	_ = os.Unsetenv(NodeTokenEnv) //nolint:errcheck // an unset of a valid name never fails
	req, frames, openErr = open()
}

// NodeRoutes returns the node routes' base URL and bearer the host passed, as
// read at initialization; both are empty outside a --sw-node-protocol process.
func NodeRoutes() (baseURL, bearer string) { return nodeURL, nodeBearer }

// Pipes returns the request and frame pipes found at initialization, or why
// there are none. It leaves HandlesEnv set for the caller to remove.
func Pipes() (request, frame *os.File, err error) {
	return req, frames, openErr
}
