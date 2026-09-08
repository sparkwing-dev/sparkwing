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

// unsupportedOperation turns a daemon's refusal into an error that names both
// versions and the way out, because the operator's fix is to replace the
// daemon rather than to change the command.
func (cl *Client) unsupportedOperation(msg wingwire.Message, operation string) error {
	refusal := cl.lacksOperation(msg)
	if refusal == nil {
		return nil
	}
	self := cl.opts.Version
	if self == "" {
		self = "(unknown)"
	}
	return fmt.Errorf("%w; this sparkwing is %s and %s needs a daemon that serves it. Stop the daemon with `sparkwing daemon restart` so the next run brings up a matching one, or run in an isolated SPARKWING_HOME",
		refusal, self, operation)
}
