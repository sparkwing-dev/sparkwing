package client

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// StartLocalDashboard waits for the elected owner's dashboard serving loop.
// An identical address reuses the listener. This never starts or replaces a
// controller. Cancellation closes the request; the owner may already be serving.
func StartLocalDashboard(ctx context.Context, home, address string) error {
	if address == "" {
		return errors.New("wingd/client: dashboard address is required")
	}
	nc, sock, err := dialLocalController(ctx, home)
	if err != nil {
		return err
	}
	defer nc.Close()
	return exchangeLocalDashboard(ctx, nc, sock, address)
}

func exchangeLocalDashboard(ctx context.Context, nc net.Conn, sock, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ownCeiling := probeDeadline(ctx)
	if err := nc.SetDeadline(deadline); err != nil {
		return err
	}
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, connDeadline: deadline}
	cancelled := make(chan struct{})
	// safety: this one-request socket is closed on cancellation so deadline changes cannot lose the wake.
	stop := context.AfterFunc(ctx, func() { defer close(cancelled); _ = nc.Close() })
	defer func() {
		if !stop() {
			<-cancelled
		}
	}()
	ack, err := cl.handshake("")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return probeFault(sock, "dashboard handshake", ownCeiling, err)
	}
	if ack.ProtocolMajor != wingwire.ProtocolMajor {
		return fmt.Errorf("wingd/client: dashboard protocol %d differs from %d", ack.ProtocolMajor, wingwire.ProtocolMajor)
	}
	cl.ack = ack
	// safety: listener readiness follows the caller's context, not the short health-probe deadline.
	deadline, _ = ctx.Deadline()
	if err := nc.SetDeadline(deadline); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	cl.connDeadline = deadline
	err = cl.startLocalDashboard(address)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("wingd/client: dashboard start %s: %w", sock, err)
	}
	return nil
}

func (cl *Client) startLocalDashboard(address string) error {
	if err := cl.write(&wingwire.LocalDashboard{Address: address}); err != nil {
		return err
	}
	message, err := cl.dec.read()
	if err != nil {
		return err
	}
	if err := cl.lacksOperation(message); err != nil {
		return err
	}
	reply, ok := message.(*wingwire.LocalDashboardAck)
	if !ok {
		return fmt.Errorf("expected local_dashboard_ack, got %T", message)
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	return nil
}
