package client

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const controllerPath = "/api/v1/local/controller/"

// QueryLocalController observes the elected owner without starting a process.
func QueryLocalController(ctx context.Context, home string) (wingd.LocalStatus, error) {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cl := &Client{sock: sock}
	var status wingd.LocalStatus
	if err := cl.apiCall(ctx, http.MethodGet, controllerPath+"status", nil, &status); err != nil {
		if errors.Is(err, context.Canceled) {
			return status, err
		}
		if dialMeansAbsent(err) {
			return status, ErrNoDaemon
		}
		return status, probeFault(sock, "local controller", true, err)
	}
	if status.PID <= 0 || status.Version == "" || status.Activity.Runs < 0 || status.Activity.QueuedTriggers < 0 || status.Activity.HeldLeases < 0 || status.Activity.DashboardConnections < 0 {
		return wingd.LocalStatus{}, errors.New("wingd/client: invalid local controller status")
	}
	return status, nil
}
