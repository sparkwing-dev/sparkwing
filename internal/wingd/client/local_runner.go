package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const localRunnerPath = "/api/v1/local/runners"

// LocalRunnerClient controls process instances through verified socket HTTP without starting a controller.
type LocalRunnerClient struct{ client *Client }

// NewLocalRunnerClient selects the existing controller serving home.
func NewLocalRunnerClient(home, version string) (*LocalRunnerClient, error) {
	socket, err := wingd.SocketPath(home)
	if err != nil {
		return nil, err
	}
	return &LocalRunnerClient{client: &Client{sock: socket, opts: Options{Home: home, Version: version}}}, nil
}

// Register persists a process as registered; it cannot claim until Activate succeeds.
func (c *LocalRunnerClient) Register(ctx context.Context, instance localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	var result localhostwire.LocalRunnerInstance
	err := c.client.apiCall(ctx, http.MethodPost, localRunnerPath, instance, &result)
	if err == nil {
		expected := instance
		expected.Machine, expected.State = result.Machine, result.State
		if result != expected {
			err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller changed runner registration identity"}
		} else {
			err = validateLocalRunnerInstance(result, instance.ID)
		}
	}
	return result, err
}

// Read returns the persisted identity and lifecycle state.
func (c *LocalRunnerClient) Read(ctx context.Context, id string) (localhostwire.LocalRunnerInstance, error) {
	var result localhostwire.LocalRunnerInstance
	err := c.client.apiCall(ctx, http.MethodGet, localRunnerPath+"/"+url.PathEscape(id), nil, &result)
	if err == nil {
		err = validateLocalRunnerInstance(result, id)
	}
	return result, err
}

// Watch waits for a lifecycle change and reconnects through transport outages without replaying claims.
func (c *LocalRunnerClient) Watch(ctx context.Context, id, state string) (localhostwire.LocalRunnerInstance, error) {
	wait := newRetry("watch local runner lifecycle", 0)
	for {
		var result localhostwire.LocalRunnerInstance
		err := c.client.apiCall(ctx, http.MethodGet, localRunnerPath+"/"+url.PathEscape(id)+"?wait_state="+url.QueryEscape(state), nil, &result)
		if err == nil {
			return result, validateLocalRunnerInstance(result, id)
		}
		if !transientAPIError(err) {
			return result, err
		}
		if err := wait.wait(ctx, err); err != nil {
			return result, err
		}
	}
}

func validateLocalRunnerInstance(instance localhostwire.LocalRunnerInstance, id string) error {
	state := instance.State
	if instance.ID != id || instance.CapabilityPrefix == "" || instance.PID <= 0 || instance.ProcessBirth == "" || instance.Version == "" || instance.BuildIdentity == "" || instance.Machine == "" || state != "registered" && state != "active" && state != "draining" && state != "stopped" {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete local runner instance"}
	}
	return nil
}

// Activate atomically enables this instance and drains its machine's previous active instance.
func (c *LocalRunnerClient) Activate(ctx context.Context, id string) error {
	return c.client.apiCall(ctx, http.MethodPost, localRunnerPath+"/"+url.PathEscape(id)+"/activate", nil, nil)
}

// Drain stops new claims while existing awards retain their reporting authority.
func (c *LocalRunnerClient) Drain(ctx context.Context, id string) error {
	return c.client.apiCall(ctx, http.MethodPost, localRunnerPath+"/"+url.PathEscape(id)+"/drain", nil, nil)
}

// Stop records that the runner has joined its hosts and terminal acknowledgements.
func (c *LocalRunnerClient) Stop(ctx context.Context, id string) error {
	return c.client.apiCall(ctx, http.MethodPost, localRunnerPath+"/"+url.PathEscape(id)+"/stop", nil, nil)
}
