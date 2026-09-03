package client

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrDaemonLacksOperation reports a daemon that does not serve a message this
// client sent. The wire surface grows by default, so this means the daemon is
// older than the operation, not that the operation is wrong: the caller
// degrades rather than failing the run.
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

// Request sends msg on the held connection and returns the daemon's answer.
//
// A daemon that does not serve msg's type answers [wingwire.Unsupported]; one
// built before that reply ends the connection without answering. Both come
// back as [ErrDaemonLacksOperation], because this exchange owns the
// connection: nothing else has been sent on it since the handshake, so a
// close with no answer is the daemon refusing the type rather than a restart
// interrupting a conversation.
func (cl *Client) Request(msg wingwire.Message) (wingwire.Message, error) {
	if err := cl.write(msg); err != nil {
		return nil, err
	}
	reply, err := cl.dec.read()
	if err != nil {
		if peerClosed(err) {
			return nil, daemonLacksOperation(string(wingwire.TypeOf(msg)), cl.ack.BinaryVersion)
		}
		return nil, err
	}
	if refusal := cl.lacksOperation(reply); refusal != nil {
		return nil, refusal
	}
	return reply, nil
}

func (cl *Client) lacksOperation(msg wingwire.Message) error {
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		return nil
	}
	return daemonLacksOperation(unsupported.Type, cl.ack.BinaryVersion)
}

func peerClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}
