package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// startLocalDashboard waits for the elected owner's dashboard serving loop.
// An identical address reuses the listener. This never starts or replaces a
// controller. Cancellation closes the request; the owner may already be serving.
func startLocalDashboard(ctx context.Context, home, address string, readOnly bool) error {
	if address == "" {
		return errors.New("wingd/client: dashboard address is required")
	}
	nc, sock, err := dialLocalController(ctx, home, probeTimeout)
	if err != nil {
		return err
	}
	defer nc.Close()
	return exchangeLocalDashboard(ctx, nc, sock, address, readOnly)
}

func exchangeLocalDashboard(ctx context.Context, nc net.Conn, sock, address string, readOnly bool) error {
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
	err = cl.startLocalDashboard(address, readOnly)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("wingd/client: dashboard start %s: %w", sock, err)
	}
	return nil
}

func (cl *Client) startLocalDashboard(address string, readOnly bool) error {
	if err := cl.write(&wingwire.LocalDashboard{Address: address, ReadOnly: readOnly}); err != nil {
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
	if reply.ReadOnly != readOnly {
		return errors.New("wingd/client: dashboard owner did not confirm the requested read-only policy; restart the controller with this version")
	}
	return nil
}

// MintDashboardLogin asks the running controller over api.sock for a one-time dashboard sign-in link.
func MintDashboardLogin(ctx context.Context, home string) (wingd.DashboardLogin, error) {
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		return wingd.DashboardLogin{}, err
	}
	client := NewAPIClient(socket)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBaseURL+wingd.DashboardLoginPath, nil)
	if err != nil {
		return wingd.DashboardLogin{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return wingd.DashboardLogin{}, &apierr.Error{Code: apierr.Unavailable, Message: "the local controller did not answer on api.sock: " + err.Error(), Hint: "run sparkwing dashboard start"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return wingd.DashboardLogin{}, err
	}
	if response.StatusCode != http.StatusOK {
		var envelope struct {
			Error *apierr.Error `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil {
			return wingd.DashboardLogin{}, envelope.Error
		}
		return wingd.DashboardLogin{}, fmt.Errorf("wingd/client: dashboard sign-in: HTTP %d", response.StatusCode)
	}
	var login wingd.DashboardLogin
	if err := json.Unmarshal(body, &login); err != nil || login.URL == "" {
		return wingd.DashboardLogin{}, errors.New("wingd/client: dashboard sign-in returned no link")
	}
	return login, nil
}
