package client

import (
	"context"
	"fmt"
	"net"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// QueryLocalController observes the elected owner without starting a process,
// taking over another version or resetting its idle deadline. An absent socket
// returns ErrNoDaemon; an unreachable or unresponsive owner is a distinct error.
func QueryLocalController(ctx context.Context, home string) (wingd.LocalStatus, error) {
	return localControl(ctx, home, nil)
}

// SetLocalStay changes the elected controller's lifetime policy. The reply must
// confirm a ready owner and the requested value. This never spawns a controller.
func SetLocalStay(ctx context.Context, home string, stay bool) (wingd.LocalStatus, error) {
	return localControl(ctx, home, &stay)
}

func localControl(ctx context.Context, home string, stay *bool) (wingd.LocalStatus, error) {
	nc, sock, err := dialLocalController(ctx, home)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	defer nc.Close()
	return exchangeLocalControl(ctx, nc, sock, stay)
}

func dialLocalController(ctx context.Context, home string) (net.Conn, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return nil, "", err
	}
	nc, err := dial(ctx, sock, probeTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if err := unreachable(sock, err); err != nil {
			return nil, "", err
		}
		return nil, "", ErrNoDaemon
	}
	return nc, sock, nil
}

func exchangeLocalControl(ctx context.Context, nc net.Conn, sock string, stay *bool) (wingd.LocalStatus, error) {
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	deadline, ownCeiling := probeDeadline(ctx)
	if err := nc.SetDeadline(deadline); err != nil {
		return wingd.LocalStatus{}, err
	}
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: stay == nil, connDeadline: deadline}
	stop := cl.cancelOnDone(ctx)
	defer stop()
	status, err := cl.localControl(stay)
	if err != nil {
		if ctx.Err() != nil {
			return wingd.LocalStatus{}, ctx.Err()
		}
		return wingd.LocalStatus{}, probeFault(sock, "local controller", ownCeiling, err)
	}
	return status, nil
}

func (cl *Client) localControl(stay *bool) (wingd.LocalStatus, error) {
	ack, err := cl.handshake("")
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	cl.ack = ack
	if ack.ProtocolMajor != wingwire.ProtocolMajor {
		return wingd.LocalStatus{}, fmt.Errorf("wingd/client: local controller protocol %d differs from %d", ack.ProtocolMajor, wingwire.ProtocolMajor)
	}
	if err := cl.write(&wingwire.LocalControl{Stay: stay}); err != nil {
		return wingd.LocalStatus{}, err
	}
	message, err := cl.dec.read()
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := cl.lacksOperation(message); err != nil {
		return wingd.LocalStatus{}, err
	}
	reply, ok := message.(*wingwire.LocalControlStatus)
	if !ok {
		return wingd.LocalStatus{}, fmt.Errorf("wingd/client: expected local_control_status, got %T", message)
	}
	if reply.PID <= 0 || reply.Version == "" || reply.Runs < 0 || reply.QueuedTriggers < 0 || reply.HeldLeases < 0 || reply.DashboardConnections < 0 {
		return wingd.LocalStatus{}, fmt.Errorf("wingd/client: invalid local controller status")
	}
	if stay != nil && (!reply.Running || reply.Stay != *stay) {
		return wingd.LocalStatus{}, fmt.Errorf("wingd/client: local controller did not acknowledge stay=%t; retry after it is ready", *stay)
	}
	return wingd.LocalStatus{
		Running: reply.Running, Stay: reply.Stay, PID: reply.PID, Version: reply.Version,
		Activity: wingd.LocalActivity{
			Runs: reply.Runs, QueuedTriggers: reply.QueuedTriggers,
			HeldLeases: reply.HeldLeases, DashboardConnections: reply.DashboardConnections, IdleSince: reply.IdleSince,
		},
	}, nil
}
