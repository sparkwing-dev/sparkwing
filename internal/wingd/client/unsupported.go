package client

import (
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrDaemonLacksOperation reports a daemon that answered [wingwire.Unsupported]
// for a message this client sent. The wire surface grows by default, so this
// means the daemon is older than the operation, not that the operation is
// wrong: the caller degrades rather than failing the run.
var ErrDaemonLacksOperation = errors.New("wingd/client: daemon does not serve this operation")

func daemonLacksOperation(wireType, daemonVersion string) error {
	if wireType == "" {
		wireType = "(unknown)"
	}
	if daemonVersion == "" {
		daemonVersion = "(unknown)"
	}
	return fmt.Errorf("%w: daemon %s does not serve %q", ErrDaemonLacksOperation, daemonVersion, wireType)
}

func (cl *Client) lacksOperation(msg wingwire.Message) error {
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		return nil
	}
	return daemonLacksOperation(unsupported.Type, cl.ack.BinaryVersion)
}
