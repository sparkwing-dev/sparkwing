package client

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// MintDashboardLogin asks the running controller over api.sock for a one-time dashboard sign-in link.
func MintDashboardLogin(ctx context.Context, home, host string) (wingd.DashboardLogin, error) {
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		return wingd.DashboardLogin{}, err
	}
	client := NewAPIClient(socket)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBaseURL+wingd.DashboardLoginPath+"?host="+url.QueryEscape(host), nil)
	if err != nil {
		return wingd.DashboardLogin{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return wingd.DashboardLogin{}, &apierr.Error{Code: hosterr.Unavailable, Message: "the local controller did not answer on api.sock: " + err.Error(), Hint: "run sparkwing dashboard open, which starts the controller"}
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
