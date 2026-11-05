package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// startLocalDashboard waits for the elected owner's dashboard serving loop.
func startLocalDashboard(ctx context.Context, home, address string, readOnly bool) error {
	if address == "" {
		return errors.New("wingd/client: dashboard address is required")
	}
	socket, err := wingd.SocketPath(home)
	if err != nil {
		return err
	}
	cl := &Client{sock: socket}
	var reply wingwire.LocalDashboardAck
	err = cl.apiCall(ctx, http.MethodPost, controllerPath+"dashboard", wingwire.LocalDashboard{Address: address, ReadOnly: readOnly}, &reply)
	if dialMeansAbsent(err) {
		return ErrNoDaemon
	}
	if err != nil {
		return err
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
