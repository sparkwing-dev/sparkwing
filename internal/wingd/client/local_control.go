package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const controllerPath = "/api/v1/local/controller/"

// QueryLocalController observes the elected owner without starting a process.
func QueryLocalController(ctx context.Context, home string) (wingd.LocalStatus, error) {
	return localControl(ctx, home, nil)
}

func setLocalStay(ctx context.Context, home string, stay bool) (wingd.LocalStatus, error) {
	return localControl(ctx, home, &stay)
}

func localControl(ctx context.Context, home string, stay *bool) (wingd.LocalStatus, error) {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cl := &Client{sock: sock}
	path, method := "status", http.MethodGet
	var input any
	if stay != nil {
		path, method = "stay", http.MethodPost
		input = struct {
			Stay bool `json:"stay"`
		}{*stay}
	}
	var status wingd.LocalStatus
	if err := cl.apiCall(ctx, method, controllerPath+path, input, &status); err != nil {
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
	if stay != nil && (!status.Running || status.Stay != *stay) {
		return status, fmt.Errorf("wingd/client: local controller did not acknowledge stay=%t; retry after it is ready", *stay)
	}
	return status, nil
}
